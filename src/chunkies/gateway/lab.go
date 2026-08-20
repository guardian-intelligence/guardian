package gateway

// The transport lab: ground truth for how a browser's WebTransport dial
// behaves against controlled server shapes, instead of theory. Enabled by
// LAB_ENABLED=true (WUM prod only), it runs four WT echo endpoints that
// share the exact QUIC configuration of the real gateway and differ in
// one variable — what the TCP side of their port does:
//
//	:4601  TCP RST at the SYN (firewall admits TCP, nothing listens)
//	:4602  TCP accept-then-close (the removed twin's behavior, kept as a specimen)
//	:4603  TCP silent drop (the firewall has no rule for it)
//	:4604  a real TLS+h2 service that does not speak WebTransport
//
// A harness page on :4599 (HTTPS, the gateway's own certificate, so any
// device anywhere gets a secure context) dials every variant crossed with
// client options ({} and {requireUnreliable:true}) plus the production
// port as control, and reports each outcome to /report, which logs one
// JSON line per result prefixed "wtlab:" — collect with kubectl logs.

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
	webtransport "github.com/quic-go/webtransport-go"
)

const labPagePort = 4599

// The lab's WT ports. TCP-side behavior is the only variable; 4603's
// silent drop is produced by the firewall (no TCP rule), not by code.
const (
	labPortRST         = 4601
	labPortAcceptClose = 4602
	labPortDrop        = 4603
	labPortH2          = 4604
	labPortHold        = 4605
)

func runLab(getCert func(*tls.ClientHelloInfo) (*tls.Certificate, error), quicConf *quic.Config) {
	for _, port := range []int{labPortRST, labPortAcceptClose, labPortDrop, labPortH2, labPortHold} {
		go labEchoServer(port, getCert, quicConf)
	}

	// The twin specimen: accept and close immediately.
	go func() {
		ln, err := net.Listen("tcp", fmt.Sprintf(":%d", labPortAcceptClose))
		if err != nil {
			log.Printf("wtlab: accept-close listener: %v", err)
			return
		}
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			log.Printf("wtlab: tcp accept-close probe from %s", conn.RemoteAddr())
			conn.Close()
		}
	}()

	// The tarpit: accept and hold the connection open, saying nothing.
	// The TCP candidate stays pending indefinitely — maximal grace for the
	// QUIC leg to win the race. (Held sockets are dropped after 60s.)
	go func() {
		ln, err := net.Listen("tcp", fmt.Sprintf(":%d", labPortHold))
		if err != nil {
			log.Printf("wtlab: accept-hold listener: %v", err)
			return
		}
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			log.Printf("wtlab: tcp accept-hold probe from %s", conn.RemoteAddr())
			go func(c net.Conn) {
				time.Sleep(60 * time.Second)
				c.Close()
			}(conn)
		}
	}()

	// A genuine TLS+h2 service with no WebTransport support.
	go func() {
		h2 := &http.Server{
			Addr: fmt.Sprintf(":%d", labPortH2),
			TLSConfig: &tls.Config{
				GetCertificate: getCert,
				NextProtos:     []string{"h2", "http/1.1"},
			},
			Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				log.Printf("wtlab: h2 request %s %s proto=%s from %s", r.Method, r.URL.Path, r.Proto, r.RemoteAddr)
				http.NotFound(w, r)
			}),
		}
		if err := h2.ListenAndServeTLS("", ""); err != nil {
			log.Printf("wtlab: h2 server: %v", err)
		}
	}()

	// The harness page and its results sink.
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		io.WriteString(w, labPage)
	})
	mux.HandleFunc("/report", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 4096))
		if err != nil || !json.Valid(body) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		log.Printf("wtlab: result %s ua=%q", body, r.UserAgent())
		w.WriteHeader(http.StatusNoContent)
	})
	page := &http.Server{
		Addr: fmt.Sprintf(":%d", labPagePort),
		TLSConfig: &tls.Config{
			GetCertificate: getCert,
			NextProtos:     []string{"h2", "http/1.1"},
		},
		Handler: mux,
	}
	log.Printf("wtlab: page on :%d, variants rst=:%d accept-close=:%d drop=:%d h2=:%d",
		labPagePort, labPortRST, labPortAcceptClose, labPortDrop, labPortH2)
	if err := page.ListenAndServeTLS("", ""); err != nil {
		log.Printf("wtlab: page server: %v", err)
	}
}

func labEchoServer(port int, getCert func(*tls.ClientHelloInfo) (*tls.Certificate, error), quicConf *quic.Config) {
	mux := http.NewServeMux()
	wt := webtransport.Server{
		H3: &http3.Server{
			Addr: fmt.Sprintf(":%d", port),
			TLSConfig: &tls.Config{
				GetCertificate: getCert,
				NextProtos:     []string{http3.NextProtoH3},
			},
			QUICConfig:      quicConf,
			Handler:         mux,
			EnableDatagrams: true,
		},
		// Diagnostics endpoint: any origin may probe it.
		CheckOrigin: func(*http.Request) bool { return true },
	}
	mux.HandleFunc("/wt", func(w http.ResponseWriter, r *http.Request) {
		sess, err := wt.Upgrade(w, r)
		if err != nil {
			log.Printf("wtlab:%d upgrade failed: %v", port, err)
			return
		}
		log.Printf("wtlab:%d session open from %s", port, r.RemoteAddr)
		go func() {
			for {
				dg, err := sess.ReceiveDatagram(r.Context())
				if err != nil {
					log.Printf("wtlab:%d session ended: %v", port, err)
					return
				}
				sess.SendDatagram(append([]byte("echo:"), dg...))
			}
		}()
	})
	if err := wt.ListenAndServe(); err != nil {
		log.Printf("wtlab:%d: %v", port, err)
	}
}

// The harness page. Dials every lab variant crossed with client options,
// production as control, 12s cap per dial, and reports every outcome.
const labPage = `<!doctype html><meta name="viewport" content="width=device-width,initial-scale=1">
<title>wt lab</title>
<body style="font:17px -apple-system,system-ui;padding:16px;max-width:640px">
<h2 style="margin:0 0 8px">WebTransport lab</h2>
<div id="out" style="white-space:pre-wrap;font-family:ui-monospace,monospace;font-size:14px"></div>
<script>
const HOST = location.hostname;
const TARGETS = [
  ["rst",          "https://" + HOST + ":4601/wt"],
  ["accept-close", "https://" + HOST + ":4602/wt"],
  ["drop",         "https://" + HOST + ":4603/wt"],
  ["h2-service",   "https://" + HOST + ":4604/wt"],
  ["accept-hold",  "https://" + HOST + ":4605/wt"],
  ["prod",         "https://" + HOST + ":4433/wt"],
];
const OPTIONS = [
  ["default", undefined],
  ["requireUnreliable", { requireUnreliable: true }],
];
const out = document.getElementById("out");
function show(line) { out.textContent += line + "\n"; }
function report(result) {
  return fetch("/report", { method: "POST", headers: { "content-type": "application/json" },
    body: JSON.stringify(result) }).catch(() => {});
}
async function dialOnce(name, url, optName, opts) {
  const started = performance.now();
  const result = { target: name, url, options: optName };
  let wt;
  try {
    wt = new WebTransport(url, opts);
  } catch (e) {
    result.outcome = "constructor-throw";
    result.ms = Math.round(performance.now() - started);
    result.error = e.name + ": " + e.message;
    return result;
  }
  const deadline = new Promise((_, reject) =>
    setTimeout(() => reject(new Error("lab deadline (12s)")), 12000));
  try {
    await Promise.race([wt.ready, deadline]);
    result.readyMs = Math.round(performance.now() - started);
    const dg = wt.datagrams;
    const writer = (dg.createWritable ? dg.createWritable() : dg.writable).getWriter();
    await writer.write(new TextEncoder().encode("lab"));
    const reader = dg.readable.getReader();
    const echo = await Promise.race([reader.read(), deadline]);
    result.outcome = "echo";
    result.ms = Math.round(performance.now() - started);
    result.echo = new TextDecoder().decode(echo.value);
  } catch (e) {
    result.outcome = result.readyMs === undefined ? "dial-failed" : "post-ready-failed";
    result.ms = Math.round(performance.now() - started);
    result.error = e.name + ": " + e.message +
      (typeof e.source === "string" ? " [source=" + e.source + "]" : "");
  }
  try { wt.close(); } catch {}
  return result;
}
(async () => {
  if (!("WebTransport" in window)) {
    show("NO WebTransport API");
    await report({ target: "api", outcome: "absent" });
    return;
  }
  const run = "run-" + Math.random().toString(16).slice(2, 8);
  show("lab " + run + " starting; " + (TARGETS.length * OPTIONS.length) + " dials, sequential\n");
  for (const [name, url] of TARGETS) {
    for (const [optName, opts] of OPTIONS) {
      show(name + " / " + optName + " …");
      const result = await dialOnce(name, url, optName, opts);
      result.run = run;
      await report(result);
      show("  → " + result.outcome + " " + result.ms + "ms" +
        (result.error ? " " + result.error : "") + (result.echo ? " " + result.echo : "") + "\n");
    }
  }
  show("done — results reported");
  await report({ run, target: "lab", outcome: "complete" });
})();
</script></body>`
