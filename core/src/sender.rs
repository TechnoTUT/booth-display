//! Per-display UDP sender: packetizes Access Units, queues them for a paced
//! send thread, answers the display's control messages (keyframe requests,
//! PING) and keeps the metrics shown in the Web GUI.

use crate::config::DisplayConfig;
use crate::protocol;
use crossbeam_channel::{bounded, select, Receiver, Sender, TryRecvError};
use serde::Serialize;
use std::net::{SocketAddr, ToSocketAddrs, UdpSocket};
use std::sync::atomic::{AtomicBool, AtomicU64, Ordering::Relaxed};
use std::sync::{Arc, Mutex};
use std::thread::JoinHandle;
use std::time::{Duration, Instant, SystemTime, UNIX_EPOCH};

/// Bounds the send queue (~360 KB, about 0.25 s of video at 12 Mbps). A deeper
/// queue only adds latency once the network falls behind.
const MAX_QUEUED_PACKETS: usize = 256;

/// Forced IDRs are at most this often per display, so a burst of losses
/// cannot turn into a stream of IDRs (docs/streaming-protocol.md).
const KEYFRAME_REQUEST_INTERVAL: Duration = Duration::from_millis(250);

/// Packets a display may get back to back before pacing holds them back.
const PACING_BURST_PACKETS: usize = 16;

/// How often the control thread checks for shutdown.
const CONTROL_POLL: Duration = Duration::from_millis(100);

#[derive(Debug, Clone, Serialize)]
pub struct DisplayMetrics {
    pub id: String,
    pub name: String,
    pub target: String,
    pub is_connected: bool,
    pub fps: f64,
    pub bitrate_kbps: f64,
    pub packets_sent: u64,
    pub bytes_sent: u64,
    pub frames_sent: u64,
    pub drops: u64,
    /// KEYFRAME_REQUESTs received from the display.
    pub keyframe_requests: u64,
    /// Unix time in milliseconds of the last packet sent, 0 if none.
    pub last_sent_unix_ms: u64,
}

#[derive(Default)]
struct Stats {
    packets_sent: AtomicU64,
    bytes_sent: AtomicU64,
    frames_sent: AtomicU64,
    drops: AtomicU64,
    keyframe_requests: AtomicU64,
    fps_counter: AtomicU64,
    byte_counter: AtomicU64,
    last_sent_unix_ms: AtomicU64,
    /// (fps, kbps) over the last second.
    rates: Mutex<(f64, f64)>,
}

/// Asks the encoder for an IDR, at most once per [`KEYFRAME_REQUEST_INTERVAL`].
#[derive(Default)]
struct KeyframeRequest {
    wanted: AtomicBool,
    last_forced: Mutex<Option<Instant>>,
}

impl KeyframeRequest {
    /// Requests an IDR unless one was forced within the interval.
    fn request(&self) {
        if self.last_forced.lock().unwrap().is_some_and(|t| t.elapsed() < KEYFRAME_REQUEST_INTERVAL) {
            return;
        }
        self.wanted.store(true, Relaxed);
    }

    /// True once per request: the encoder makes its next frame an IDR.
    fn take(&self) -> bool {
        if !self.wanted.swap(false, Relaxed) {
            return false;
        }
        *self.last_forced.lock().unwrap() = Some(Instant::now());
        true
    }
}

/// Spaces packets out to a bit rate, letting [`PACING_BURST_PACKETS`] full
/// packets through back to back after an idle period.
struct Pacer {
    ns_per_byte: f64,
    burst: Duration,
    next: Instant,
}

impl Pacer {
    /// None when pacing is off.
    fn new(rate_mbps: Option<f64>) -> Option<Self> {
        let ns_per_byte = 8_000.0 / rate_mbps?;
        let full = protocol::HEADER_SIZE + protocol::MAX_PAYLOAD + 28;
        let burst = Duration::from_nanos((ns_per_byte * (PACING_BURST_PACKETS * full) as f64) as u64);
        Some(Self { ns_per_byte, burst, next: Instant::now() })
    }

    /// Waits until a packet of `len` bytes may leave.
    fn pace(&mut self, len: usize) {
        let now = Instant::now();
        // Idle time builds up at most one burst of credit.
        self.next = self.next.max(now.checked_sub(self.burst).unwrap_or(now));
        if self.next > now {
            std::thread::sleep(self.next - now);
        }
        // Advance on the schedule, not on the clock, so oversleeping is made
        // up by the following packets.
        self.next += Duration::from_nanos((self.ns_per_byte * (len + 28) as f64) as u64);
    }
}

/// Frame admission into the packet queue. Only one thread may call
/// [`FrameQueue::send_frame`] at a time; that guarantees the free space it
/// checks cannot be taken by another producer.
struct FrameQueue {
    tx: Sender<Vec<u8>>,
    stop: Receiver<()>,
    state: Mutex<ProducerState>,
    keyframe: Arc<KeyframeRequest>,
}

#[derive(Default)]
struct ProducerState {
    need_keyframe: bool,
    seq: u32,
}

impl FrameQueue {
    /// Splits a frame into packets and queues them.
    ///
    /// A frame is admitted only if all of its packets fit in the queue;
    /// otherwise the whole frame is dropped. Packet-level drops would corrupt
    /// frames at the receiver, and after any drop P-frames are skipped until
    /// the next keyframe so the decoder never references a missing picture.
    /// The drop also asks the encoder for that keyframe.
    ///
    /// A frame larger than the whole queue is admitted when the queue is
    /// empty, and the caller waits while the send thread drains it. Without
    /// this, such a keyframe could never be sent and the stream would stall.
    fn send_frame(&self, stats: &Stats, data: &[u8], payload_type: u8, keyframe: bool, ts: u32) {
        let n = protocol::packet_count(data.len()) as u64;
        let mut st = self.state.lock().unwrap();
        if st.need_keyframe && !keyframe {
            stats.drops.fetch_add(n, Relaxed);
            return;
        }
        let queued = self.tx.len();
        let cap = self.tx.capacity().unwrap_or(usize::MAX);
        if n as usize > cap - queued && queued > 0 {
            stats.drops.fetch_add(n, Relaxed);
            st.need_keyframe = true;
            self.keyframe.request();
            return;
        }
        st.need_keyframe = false;

        let packets = protocol::packetize(data, payload_type, keyframe, &mut st.seq, ts);
        drop(st);
        stats.frames_sent.fetch_add(1, Relaxed);
        stats.fps_counter.fetch_add(1, Relaxed);
        for pkt in packets {
            select! {
                send(self.tx, pkt) -> r => if r.is_err() { return },
                // The send thread is gone; an oversized frame would otherwise block forever.
                recv(self.stop) -> _ => return,
            }
        }
    }
}

pub struct DisplaySender {
    cfg: DisplayConfig,
    target: SocketAddr,
    queue: FrameQueue,
    stats: Arc<Stats>,
    stop_tx: Mutex<Option<Sender<()>>>,
    threads: Mutex<Vec<JoinHandle<()>>>,
}

impl DisplaySender {
    /// Opens the display's socket. `rate_mbps` paces its packets (None: off).
    pub fn new(cfg: &DisplayConfig, rate_mbps: Option<f64>) -> anyhow::Result<Arc<Self>> {
        let target = (cfg.ip.as_str(), cfg.port)
            .to_socket_addrs()
            .map_err(|e| anyhow::anyhow!("failed to resolve UDP target {}:{}: {e}", cfg.ip, cfg.port))?
            .next()
            .ok_or_else(|| anyhow::anyhow!("no address for UDP target {}:{}", cfg.ip, cfg.port))?;
        let bind: SocketAddr = if target.is_ipv4() { "0.0.0.0:0".parse()? } else { "[::]:0".parse()? };
        let sock = UdpSocket::bind(bind)?;
        sock.connect(target)?;

        let (tx, rx) = bounded::<Vec<u8>>(MAX_QUEUED_PACKETS);
        let (stop_tx, stop_rx) = bounded::<()>(0);
        let stats = Arc::new(Stats::default());
        let keyframe = Arc::new(KeyframeRequest::default());

        let send_thread = {
            let (stats, stop, sock) = (stats.clone(), stop_rx.clone(), sock.try_clone()?);
            let mut pacer = Pacer::new(rate_mbps);
            std::thread::Builder::new().name(format!("udp-{}", cfg.id)).spawn(move || loop {
                select! {
                    recv(rx) -> pkt => {
                        let Ok(pkt) = pkt else { return };
                        if let Some(p) = pacer.as_mut() {
                            p.pace(pkt.len());
                        }
                        // A refused send (no listener yet) is not fatal; keep going.
                        if let Ok(n) = sock.send(&pkt) {
                            stats.packets_sent.fetch_add(1, Relaxed);
                            stats.bytes_sent.fetch_add(n as u64, Relaxed);
                            stats.byte_counter.fetch_add(n as u64, Relaxed);
                            stats.last_sent_unix_ms.store(unix_ms(), Relaxed);
                        }
                    }
                    recv(stop) -> _ => return,
                }
            })?
        };
        // The socket is connected, so only the display's own packets arrive.
        let control_thread = {
            let (stats, stop, keyframe) = (stats.clone(), stop_rx.clone(), keyframe.clone());
            sock.set_read_timeout(Some(CONTROL_POLL))?;
            std::thread::Builder::new().name(format!("udp-ctl-{}", cfg.id)).spawn(move || {
                let mut buf = [0u8; 64];
                while !matches!(stop.try_recv(), Err(TryRecvError::Disconnected)) {
                    // Timeouts, and refusals while the display is not listening, are expected.
                    let Ok(n) = sock.recv(&mut buf) else { continue };
                    let Some((h, _)) = protocol::PacketHeader::parse(&buf[..n]) else { continue };
                    match h.payload_type {
                        protocol::PAYLOAD_KEYFRAME_REQUEST => {
                            stats.keyframe_requests.fetch_add(1, Relaxed);
                            keyframe.request();
                        }
                        protocol::PAYLOAD_PING => {
                            let _ = sock.send(&protocol::pong(&h, protocol::now_us()));
                        }
                        _ => {}
                    }
                }
            })?
        };
        let metrics_thread = {
            let (stats, stop) = (stats.clone(), stop_rx.clone());
            std::thread::Builder::new().name(format!("metrics-{}", cfg.id)).spawn(move || {
                while stop.recv_timeout(Duration::from_secs(1)).is_err_and(|e| e.is_timeout()) {
                    let fps = stats.fps_counter.swap(0, Relaxed);
                    let bytes = stats.byte_counter.swap(0, Relaxed);
                    *stats.rates.lock().unwrap() = (fps as f64, (bytes * 8) as f64 / 1000.0);
                }
            })?
        };

        Ok(Arc::new(Self {
            cfg: cfg.clone(),
            target,
            queue: FrameQueue { tx, stop: stop_rx, state: Mutex::default(), keyframe },
            stats,
            stop_tx: Mutex::new(Some(stop_tx)),
            threads: Mutex::new(vec![send_thread, control_thread, metrics_thread]),
        }))
    }

    pub fn send_frame(&self, data: &[u8], payload_type: u8, keyframe: bool, ts: u32) {
        self.queue.send_frame(&self.stats, data, payload_type, keyframe, ts);
    }

    /// True when the display (or a dropped frame) asked for an IDR; the
    /// encoder then makes its next frame one.
    pub fn take_keyframe_request(&self) -> bool {
        self.queue.keyframe.take()
    }

    pub fn metrics(&self) -> DisplayMetrics {
        let (fps, kbps) = *self.stats.rates.lock().unwrap();
        DisplayMetrics {
            id: self.cfg.id.clone(),
            name: self.cfg.name.clone(),
            target: self.target.to_string(),
            is_connected: true,
            fps,
            bitrate_kbps: kbps,
            packets_sent: self.stats.packets_sent.load(Relaxed),
            bytes_sent: self.stats.bytes_sent.load(Relaxed),
            frames_sent: self.stats.frames_sent.load(Relaxed),
            drops: self.stats.drops.load(Relaxed),
            keyframe_requests: self.stats.keyframe_requests.load(Relaxed),
            last_sent_unix_ms: self.stats.last_sent_unix_ms.load(Relaxed),
        }
    }

    pub fn close(&self) {
        // Dropping the stop sender wakes every thread waiting on it.
        self.stop_tx.lock().unwrap().take();
        for t in self.threads.lock().unwrap().drain(..) {
            let _ = t.join();
        }
    }
}

impl Drop for DisplaySender {
    fn drop(&mut self) {
        self.close();
    }
}

fn unix_ms() -> u64 {
    SystemTime::now().duration_since(UNIX_EPOCH).map_or(0, |d| d.as_millis() as u64)
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::time::Instant;

    /// A queue with no send thread: it only drains when the test reads it.
    fn test_queue(cap: usize) -> (FrameQueue, Receiver<Vec<u8>>, Sender<()>) {
        let (tx, rx) = bounded(cap);
        let (stop_tx, stop_rx) = bounded(0);
        let q = FrameQueue { tx, stop: stop_rx, state: Mutex::default(), keyframe: Arc::default() };
        (q, rx, stop_tx)
    }

    fn frame(packets: usize) -> Vec<u8> {
        vec![0; packets * protocol::MAX_PAYLOAD]
    }

    const H264: u8 = protocol::PAYLOAD_H264;

    #[test]
    fn queues_whole_frame() {
        let (q, rx, _stop) = test_queue(8);
        let s = Stats::default();
        q.send_frame(&s, &frame(3), H264, true, 0);
        assert_eq!(rx.len(), 3);
        assert_eq!(s.drops.load(Relaxed), 0);
    }

    #[test]
    fn drops_whole_frame_and_resyncs_on_keyframe() {
        let (q, rx, _stop) = test_queue(4);
        let s = Stats::default();
        q.send_frame(&s, &frame(3), H264, true, 0);
        // Only 1 slot left: the 2-packet frame is dropped as a whole.
        q.send_frame(&s, &frame(2), H264, false, 1);
        assert_eq!(rx.len(), 3);
        assert_eq!(s.drops.load(Relaxed), 2);
        // The drop asks the encoder for the keyframe it now waits for.
        assert!(q.keyframe.take());
        assert!(!q.keyframe.take());

        // After the queue drains, P-frames are still skipped until a keyframe.
        while rx.try_recv().is_ok() {}
        q.send_frame(&s, &frame(1), H264, false, 2);
        assert_eq!(rx.len(), 0);

        q.send_frame(&s, &frame(2), H264, true, 3);
        q.send_frame(&s, &frame(1), H264, false, 4);
        assert_eq!(rx.len(), 3);
        assert_eq!(s.drops.load(Relaxed), 3);
    }

    #[test]
    fn admits_oversized_frame_when_queue_empty() {
        let (q, rx, _stop) = test_queue(4);
        let s = Stats::default();
        let reader = std::thread::spawn(move || (0..10).filter(|_| rx.recv_timeout(Duration::from_secs(1)).is_ok()).count());
        q.send_frame(&s, &frame(10), H264, true, 0);
        assert_eq!(reader.join().unwrap(), 10);
        assert_eq!(s.drops.load(Relaxed), 0);
        assert!(!q.state.lock().unwrap().need_keyframe);
    }

    #[test]
    fn drops_oversized_frame_when_queue_busy() {
        let (q, rx, _stop) = test_queue(4);
        let s = Stats::default();
        q.send_frame(&s, &frame(1), H264, true, 0);
        q.send_frame(&s, &frame(10), H264, true, 1);
        assert_eq!(rx.len(), 1);
        assert_eq!(s.drops.load(Relaxed), 10);
    }

    #[test]
    fn keyframe_requests_are_rate_limited() {
        let k = KeyframeRequest::default();
        k.request();
        assert!(k.take());
        // Inside the interval after a forced IDR: ignored.
        k.request();
        assert!(!k.take());
        *k.last_forced.lock().unwrap() = Some(Instant::now() - KEYFRAME_REQUEST_INTERVAL);
        k.request();
        assert!(k.take());
    }

    #[test]
    fn pacer_holds_packets_to_the_rate() {
        // 100 full packets at 80 Mbps: the 84 after the burst take at least 12 ms.
        let full = protocol::HEADER_SIZE + protocol::MAX_PAYLOAD;
        let mut p = Pacer::new(Some(80.0)).unwrap();
        let start = Instant::now();
        for _ in 0..100 {
            p.pace(full);
        }
        let want = Duration::from_nanos(((100 - PACING_BURST_PACKETS) * (full + 28) * 100) as u64);
        assert!(start.elapsed() >= want, "{:?} < {want:?}", start.elapsed());
        assert!(Pacer::new(None).is_none());
    }

    #[test]
    fn answers_keyframe_requests_and_pings() {
        let display = UdpSocket::bind("127.0.0.1:0").unwrap();
        display.set_read_timeout(Some(Duration::from_secs(2))).unwrap();
        let cfg = DisplayConfig {
            id: "t".into(),
            ip: "127.0.0.1".into(),
            port: display.local_addr().unwrap().port(),
            ..Default::default()
        };
        let sender = DisplaySender::new(&cfg, None).unwrap();

        // The display learns the controller's address from a video packet.
        sender.send_frame(&[0, 0, 0, 1, 9], protocol::PAYLOAD_H264, true, protocol::now_us());
        let mut buf = [0u8; 2048];
        let (_, controller) = display.recv_from(&mut buf).unwrap();

        let control = |payload_type, sequence, timestamp| {
            let mut pkt = [0u8; protocol::HEADER_SIZE];
            protocol::PacketHeader { payload_type, flags: 0, sequence, timestamp, fragment_index: 0, fragment_total: 1 }
                .write(&mut pkt);
            display.send_to(&pkt, controller).unwrap();
        };

        control(protocol::PAYLOAD_PING, 5, 1234);
        let n = display.recv(&mut buf).unwrap();
        let (h, payload) = protocol::PacketHeader::parse(&buf[..n]).unwrap();
        assert_eq!((h.payload_type, h.sequence), (protocol::PAYLOAD_PONG, 5));
        assert_eq!(payload, 1234u32.to_be_bytes());

        control(protocol::PAYLOAD_KEYFRAME_REQUEST, 0, 0);
        let deadline = Instant::now() + Duration::from_secs(2);
        while !sender.take_keyframe_request() {
            assert!(Instant::now() < deadline, "keyframe request not seen");
            std::thread::sleep(Duration::from_millis(5));
        }
        assert_eq!(sender.metrics().keyframe_requests, 1);
    }

    #[test]
    fn does_not_block_after_stop() {
        let (q, _rx, stop) = test_queue(4);
        drop(stop);
        let s = Stats::default();
        let start = Instant::now();
        q.send_frame(&s, &frame(10), H264, true, 0);
        assert!(start.elapsed() < Duration::from_secs(1));
    }
}
