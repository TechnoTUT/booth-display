//! booth-core: the media core of booth-display.
//!
//! The Go controller starts `booth-core engine` as a child process and drives
//! it over a control protocol in the style of moip-engine's `--control` mode:
//!
//! * stdin: one JSON command per line, e.g. `{"id":1,"cmd":"start","mode":"testpattern"}`.
//!   EOF (the controller went away) is treated as `quit`.
//! * stdout: framed messages (see `output.rs`): JSON events and preview JPEGs.
//! * stderr: human-readable log lines.
//!
//! Every command that carries an `id` gets a `{"type":"reply","id":..,"ok":..}`
//! event; a `status` event is sent periodically and after every command.

mod config;
mod encoder;
mod engine;
mod ffsource;
mod flv;
mod frame;
mod logo;
mod mixer;
mod nv12;
mod ndi_input;
mod output;
mod preview;
mod protocol;
mod scale;
mod sender;
mod testpattern;
mod vaapi;
mod vaenc;

use config::{CanvasConfig, DisplayConfig, PipelineConfig};
use engine::Engine;
use mixer::Transition;
use serde::Deserialize;
use serde_json::json;
use std::io::BufRead;
use std::sync::{Arc, Mutex};
use std::time::Duration;

const STATUS_INTERVAL: Duration = Duration::from_millis(500);

#[derive(Debug, Deserialize)]
#[serde(tag = "cmd", rename_all = "snake_case")]
enum Command {
    Configure {
        #[serde(default)]
        canvas: CanvasConfig,
        #[serde(default)]
        displays: Vec<DisplayConfig>,
        #[serde(default)]
        pipeline: PipelineConfig,
    },
    Start {
        #[serde(default)]
        mode: String,
        #[serde(default)]
        target: String,
    },
    Stop,
    Switch {
        #[serde(default)]
        source_type: String,
        #[serde(default)]
        target: String,
        #[serde(default)]
        transition: Option<Transition>,
        #[serde(default)]
        duration_ms: u64,
    },
    /// List NDI sources (may take a few seconds right after startup).
    Discover,
    Quit,
}

#[derive(Debug, Deserialize)]
struct Request {
    #[serde(default)]
    id: Option<u64>,
    #[serde(flatten)]
    cmd: Command,
}

fn reply(id: Option<u64>, result: anyhow::Result<serde_json::Value>) {
    let Some(id) = id else {
        if let Err(e) = result {
            eprintln!("[core] {e:#}");
        }
        return;
    };
    let mut v = match result {
        Ok(serde_json::Value::Object(m)) => serde_json::Value::Object(m),
        Ok(_) => json!({}),
        Err(e) => json!({ "error": format!("{e:#}") }),
    };
    let ok = v.get("error").is_none();
    v["type"] = "reply".into();
    v["id"] = id.into();
    v["ok"] = ok.into();
    output::event(&v);
}

fn send_status(engine: &Mutex<Engine>) {
    let st = engine.lock().unwrap().status();
    let mut v = serde_json::to_value(st).unwrap_or_default();
    v["type"] = "status".into();
    output::event(&v);
}

fn run_engine() {
    output::init();
    // Fix glibc's mmap/trim thresholds so freed frame buffers go back to the
    // OS (as moip-engine does).
    // SAFETY: plain glibc calls at startup.
    unsafe {
        libc::mallopt(libc::M_MMAP_THRESHOLD, 1 << 20);
        libc::mallopt(libc::M_TRIM_THRESHOLD, 4 << 20);
    }
    let engine = Arc::new(Mutex::new(Engine::new()));
    {
        let engine = engine.clone();
        std::thread::Builder::new()
            .name("status".into())
            .spawn(move || loop {
                std::thread::sleep(STATUS_INTERVAL);
                send_status(&engine);
            })
            .expect("spawn status thread");
    }
    output::event(&json!({ "type": "ready", "version": env!("CARGO_PKG_VERSION") }));

    for line in std::io::stdin().lock().lines() {
        let Ok(line) = line else { break };
        let line = line.trim();
        if line.is_empty() {
            continue;
        }
        let req: Request = match serde_json::from_str(line) {
            Ok(r) => r,
            Err(e) => {
                eprintln!("[core] ignoring bad command {line:?}: {e}");
                let id = serde_json::from_str::<serde_json::Value>(line).ok().and_then(|v| v["id"].as_u64());
                reply(id, Err(anyhow::anyhow!("bad command: {e}")));
                continue;
            }
        };
        let id = req.id;
        let result = match req.cmd {
            Command::Quit => break,
            Command::Discover => {
                // Discovery can wait for seconds; keep other commands flowing.
                std::thread::spawn(move || {
                    reply(id, ndi_input::discover().map(|s| json!({ "sources": s })));
                });
                continue;
            }
            Command::Configure { canvas, displays, pipeline } => {
                engine.lock().unwrap().configure(canvas, displays, pipeline).map(|_| json!({}))
            }
            Command::Start { mode, target } => engine.lock().unwrap().start(&mode, &target).map(|_| json!({})),
            Command::Stop => {
                engine.lock().unwrap().stop();
                Ok(json!({}))
            }
            Command::Switch { source_type, target, transition, duration_ms } => engine
                .lock()
                .unwrap()
                .switch(&source_type, &target, transition.unwrap_or_default(), duration_ms)
                .map(|_| json!({})),
        };
        // Status first: once the controller sees the reply, its status is current.
        send_status(&engine);
        reply(id, result);
    }
    engine.lock().unwrap().stop();
}

fn usage() -> ! {
    eprintln!("usage: booth-core engine     run under the controller (JSON commands on stdin)");
    eprintln!("       booth-core discover   list NDI sources as JSON");
    eprintln!("       booth-core version    print versions");
    std::process::exit(2);
}

fn main() {
    match std::env::args().nth(1).as_deref() {
        Some("engine") | None => run_engine(),
        Some("discover") => match ndi_input::discover() {
            Ok(s) => println!("{}", serde_json::to_string_pretty(&s).unwrap()),
            Err(e) => {
                eprintln!("{e:#}");
                std::process::exit(1);
            }
        },
        Some("version") => {
            println!("booth-core {}", env!("CARGO_PKG_VERSION"));
            match ndi::version() {
                Ok(v) => println!("NDI {v}"),
                Err(e) => println!("NDI runtime not available: {e}"),
            }
        }
        _ => usage(),
    }
}
