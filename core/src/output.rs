//! Messages to the controller on stdout, framed like moip-engine's preview
//! output so binary JPEGs and JSON events share one pipe:
//!
//! ```text
//! [kind: u8][flags: u8][len: u32 little-endian][payload: len bytes]
//! kind 0 = JSON event, 2 = JPEG preview frame
//! ```
//!
//! A single thread writes stdout. JSON events are never dropped; preview
//! frames are dropped when the controller falls behind.

use crossbeam_channel::{bounded, unbounded, Receiver, Sender};
use serde::Serialize;
use std::io::Write;
use std::sync::OnceLock;

pub const KIND_JSON: u8 = 0;
pub const KIND_JPEG: u8 = 2;

struct Out {
    events: Sender<Vec<u8>>,
    jpegs: Sender<Vec<u8>>,
}

static OUT: OnceLock<Out> = OnceLock::new();

/// Starts the stdout writer thread. Further calls are no-ops.
pub fn init() {
    OUT.get_or_init(|| {
        let (events, ev_rx) = unbounded::<Vec<u8>>();
        let (jpegs, jp_rx) = bounded::<Vec<u8>>(2);
        std::thread::Builder::new()
            .name("stdout".into())
            .spawn(move || writer(ev_rx, jp_rx))
            .expect("spawn stdout writer");
        Out { events, jpegs }
    });
}

fn writer(events: Receiver<Vec<u8>>, jpegs: Receiver<Vec<u8>>) {
    let stdout = std::io::stdout();
    let mut w = std::io::BufWriter::new(stdout.lock());
    loop {
        let (kind, payload) = crossbeam_channel::select_biased! {
            recv(events) -> m => match m { Ok(m) => (KIND_JSON, m), Err(_) => return },
            recv(jpegs) -> m => match m { Ok(m) => (KIND_JPEG, m), Err(_) => return },
        };
        let mut hdr = [kind, 0, 0, 0, 0, 0];
        hdr[2..].copy_from_slice(&(payload.len() as u32).to_le_bytes());
        // The controller went away: exit like on stdin EOF.
        if w.write_all(&hdr).and_then(|_| w.write_all(&payload)).and_then(|_| w.flush()).is_err() {
            std::process::exit(0);
        }
    }
}

/// Sends a JSON event (`{"type": ..., ...}`).
pub fn event(v: &impl Serialize) {
    if let (Some(o), Ok(json)) = (OUT.get(), serde_json::to_vec(v)) {
        let _ = o.events.send(json);
    }
}

/// Sends a preview JPEG unless two are already waiting.
pub fn jpeg(data: Vec<u8>) {
    if let Some(o) = OUT.get() {
        let _ = o.jpegs.try_send(data);
    }
}
