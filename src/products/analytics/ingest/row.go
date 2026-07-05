package main

import (
	"net/http"
	"net/netip"
	"strings"
	"time"
)

// One row of guardian_analytics.events
// (src/infrastructure/analytics/events-table.sql). Everything except the
// client-claimed content fields is derived server-side from the request.
type eventRow struct {
	ServerTs      time.Time
	Site          string
	EventName     string
	TrustTier     uint8
	SchemaVersion uint8
	TraceID       [16]byte
	CorrelationID [16]byte
	SessionSeq    uint32
	Path          string
	Referrer      string
	UA            string
	ClientIP      netip.Addr
	IPSource      string
	// Country/ASN stay zero until the GeoIP/ASN lookup lands (needs an MMDB
	// source + refresh story). The design doc promises server derivation —
	// deferred, not dropped.
	Country string
	ASN     uint32
	Status        uint16
	DurationMs    uint32
	ClientSkewMs  int32
	VitalName     string
	VitalValue    float64
	Props         string
}

const (
	tierServerObserved = 1
	tierEdgeVerified   = 2
	tierClientClaimed  = 3
)

// Headers stamped by the tenant-root ingress controller (PR #375). The maps
// behind them are keyed on $ssl_client_verify, so a value only ever appears
// when the TLS client cert chain verified against the Cloudflare origin-pull
// CA; proxySetHeaders overwrites any client-supplied copy on every request
// that transits the controller.
const (
	headerClientIP       = "x-guardian-client-ip"
	headerClientIPSource = "x-guardian-client-ip-source"
)

// requestContext derives the per-request (per-batch) row fields shared by
// every event in a Publish call.
type requestContext struct {
	Site      string
	TrustTier uint8
	ClientIP  netip.Addr
	IPSource  string
	UA        string
}

func deriveRequestContext(r *http.Request) requestContext {
	ctx := requestContext{
		Site:      siteFromHost(r.Host),
		TrustTier: tierClientClaimed,
		UA:        truncate(r.UserAgent(), 512),
		ClientIP:  netip.IPv6Unspecified(),
	}
	source := r.Header.Get(headerClientIPSource)
	rawIP := r.Header.Get(headerClientIP)
	if source == "cloudflare" && rawIP != "" {
		if addr, err := netip.ParseAddr(rawIP); err == nil {
			ctx.ClientIP = mapToV6(addr)
			ctx.IPSource = source
			ctx.TrustTier = tierEdgeVerified
		}
	}
	return ctx
}

// siteFromHost maps the serving host to the site column: the apex is prod,
// any other guardianintelligence.org label names itself (pr-12, beta, ...),
// anything else (port-forwards, cluster-internal probes) is local.
func siteFromHost(host string) string {
	h := strings.ToLower(host)
	if i := strings.IndexByte(h, ':'); i >= 0 {
		h = h[:i]
	}
	const zone = "guardianintelligence.org"
	if h == zone || h == "www."+zone {
		return "prod"
	}
	if strings.HasSuffix(h, "."+zone) {
		return truncate(strings.TrimSuffix(h, "."+zone), 32)
	}
	return "local"
}

func mapToV6(a netip.Addr) netip.Addr {
	if a.Is4() {
		return netip.AddrFrom16(a.As16())
	}
	return a
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
