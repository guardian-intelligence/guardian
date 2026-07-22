//! Minimal HTTP/1.1 server for exercising the device flow against canned
//! responses. Every response carries `Connection: close` so the client
//! reconnects per request and each `accept` maps to exactly one exchange.

use std::io::{BufRead, BufReader, Read, Write};
use std::net::{TcpListener, TcpStream};
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
