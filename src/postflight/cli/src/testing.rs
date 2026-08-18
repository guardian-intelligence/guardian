//! Minimal HTTP/1.1 server for exercising the OAuth calls against canned
//! responses, plus the scratch state the credential tests need. Every response
//! carries `Connection: close` so the client reconnects per request and each
//! `accept` maps to exactly one exchange.

use std::io::{BufRead, BufReader, Read, Write};
use std::net::{TcpListener, TcpStream};
use std::path::{Path, PathBuf};
use std::sync::atomic::{AtomicU64, Ordering};
use std::thread::JoinHandle;

pub struct TestServer {
    pub url: String,
    handle: JoinHandle<Vec<String>>,
}

impl TestServer {
    pub fn serve(responses: Vec<String>) -> Self {
        let listener = TcpListener::bind("127.0.0.1:0").expect("bind test listener");
        let addr = listener.local_addr().expect("test listener addr");
        let handle = std::thread::spawn(move || {
            let mut captured = Vec::new();
            for response in responses {
                let (stream, _) = listener.accept().expect("accept test connection");
                captured.push(read_request(&stream));
                (&stream)
                    .write_all(response.as_bytes())
                    .expect("write test response");
            }
            captured
        });
        Self {
            url: format!("http://{addr}"),
            handle,
        }
    }

    /// An address nothing is listening on: the kernel hands out the port and
    /// the listener is closed before anything connects to it.
    pub fn unreachable_url() -> String {
        let listener = TcpListener::bind("127.0.0.1:0").expect("bind test listener");
        let addr = listener.local_addr().expect("test listener addr");
        drop(listener);
        format!("http://{addr}")
    }

    /// Join the server thread and return the raw requests it captured.
    pub fn finish(self) -> Vec<String> {
        self.handle.join().expect("test server thread")
    }
}

fn read_request(stream: &TcpStream) -> String {
    let mut reader = BufReader::new(stream);
    let mut head = String::new();
    let mut content_length = 0usize;
    loop {
        let mut line = String::new();
        reader.read_line(&mut line).expect("read request line");
        if let Some(value) = line.to_ascii_lowercase().strip_prefix("content-length:") {
            content_length = value.trim().parse().unwrap_or(0);
        }
        let end_of_headers = line == "\r\n" || line == "\n";
        head.push_str(&line);
        if end_of_headers {
            break;
        }
    }
    let mut body = vec![0u8; content_length];
    reader.read_exact(&mut body).expect("read request body");
    head + &String::from_utf8_lossy(&body)
}

pub fn json_response(status: u16, body: &str) -> String {
    format!(
        "HTTP/1.1 {status} Status\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{body}",
        body.len()
    )
}

pub fn no_content_response() -> String {
    String::from("HTTP/1.1 204 No Content\r\nConnection: close\r\n\r\n")
}

pub fn agent() -> ureq::Agent {
    ureq::Agent::new_with_config(
        ureq::Agent::config_builder()
            .http_status_as_error(false)
            .build(),
    )
}

/// A directory of its own per test, removed when the test drops it, so the
/// credential tests never observe each other's files.
pub struct ScratchDir(PathBuf);

static SCRATCH_SEQUENCE: AtomicU64 = AtomicU64::new(0);

pub fn scratch_dir(name: &str) -> ScratchDir {
    let unique = SCRATCH_SEQUENCE.fetch_add(1, Ordering::Relaxed);
    let path = std::env::temp_dir().join(format!(
        "postflight-cli-test-{}-{unique}-{name}",
        std::process::id()
    ));
    std::fs::remove_dir_all(&path).ok();
    ScratchDir(path)
}

impl std::ops::Deref for ScratchDir {
    type Target = Path;

    fn deref(&self) -> &Path {
        &self.0
    }
}

impl AsRef<Path> for ScratchDir {
    fn as_ref(&self) -> &Path {
        &self.0
    }
}

impl Drop for ScratchDir {
    fn drop(&mut self) {
        std::fs::remove_dir_all(&self.0).ok();
    }
}
