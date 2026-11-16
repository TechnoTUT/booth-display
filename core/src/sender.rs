//! Per-display UDP sender: packetizes Access Units, queues them for a send
//! thread and keeps the metrics shown in the Web GUI.

use crate::config::DisplayConfig;
use crate::protocol;
use crossbeam_channel::{bounded, select, Receiver, Sender};
use serde::Serialize;
use std::net::{SocketAddr, ToSocketAddrs, UdpSocket};
use std::sync::atomic::{AtomicU64, Ordering::Relaxed};
use std::sync::{Arc, Mutex};
use std::thread::JoinHandle;
use std::time::{Duration, SystemTime, UNIX_EPOCH};

/// Bounds the send queue (~360 KB, about 0.25 s of video at 12 Mbps). A deeper
/// queue only adds latency once the network falls behind.
const MAX_QUEUED_PACKETS: usize = 256;

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
    /// Unix time in milliseconds of the last packet sent, 0 if none.
    pub last_sent_unix_ms: u64,
}

#[derive(Default)]
struct Stats {
    packets_sent: AtomicU64,
    bytes_sent: AtomicU64,
    frames_sent: AtomicU64,
    drops: AtomicU64,
    fps_counter: AtomicU64,
    byte_counter: AtomicU64,
    last_sent_unix_ms: AtomicU64,
    /// (fps, kbps) over the last second.
    rates: Mutex<(f64, f64)>,
}

/// Frame admission into the packet queue. Only one thread may call
/// [`FrameQueue::send_frame`] at a time; that guarantees the free space it
/// checks cannot be taken by another producer.
struct FrameQueue {
    tx: Sender<Vec<u8>>,
    stop: Receiver<()>,
    state: Mutex<ProducerState>,
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
    pub fn new(cfg: &DisplayConfig) -> anyhow::Result<Arc<Self>> {
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

        let send_thread = {
            let (stats, stop) = (stats.clone(), stop_rx.clone());
            std::thread::Builder::new().name(format!("udp-{}", cfg.id)).spawn(move || loop {
                select! {
                    recv(rx) -> pkt => {
                        let Ok(pkt) = pkt else { return };
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
            queue: FrameQueue { tx, stop: stop_rx, state: Mutex::default() },
            stats,
            stop_tx: Mutex::new(Some(stop_tx)),
            threads: Mutex::new(vec![send_thread, metrics_thread]),
        }))
    }

    pub fn send_frame(&self, data: &[u8], payload_type: u8, keyframe: bool, ts: u32) {
        self.queue.send_frame(&self.stats, data, payload_type, keyframe, ts);
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
        (FrameQueue { tx, stop: stop_rx, state: Mutex::default() }, rx, stop_tx)
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
    fn does_not_block_after_stop() {
        let (q, _rx, stop) = test_queue(4);
        drop(stop);
        let s = Stats::default();
        let start = Instant::now();
        q.send_frame(&s, &frame(10), H264, true, 0);
        assert!(start.elapsed() < Duration::from_secs(1));
    }
}
