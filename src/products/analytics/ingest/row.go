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
	DeviceClass   string
	OSFamily      string
	BrowserFamily string
	ClientIP      netip.Addr
	IPSource      string
	Country       string
	ASN           uint32
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

// tierLabel mirrors the trust_tier Enum8 in events-table.sql; the
// observability alerts match on these strings.
func tierLabel(t uint8) string {
	switch t {
	case tierServerObserved:
		return "server_observed"
	case tierEdgeVerified:
		return "edge_verified"
	case tierClientClaimed:
		return "client_claimed"
	}
	return "unspecified"
}

// Headers stamped by the tenant-root ingress controller (PR #375). The maps
// behind them are keyed on $ssl_client_verify, so a value only ever appears
// when the TLS client cert chain verified against the Cloudflare origin-pull
// CA; proxySetHeaders overwrites any client-supplied copy on every request
// that transits the controller.
const (
	headerClientIP       = "x-guardian-client-ip"
	headerClientIPSource = "x-guardian-client-ip-source"
	headerClientCountry  = "x-guardian-client-country"
)

// requestContext derives the per-request (per-batch) row fields shared by
// every event in a Publish call.
type requestContext struct {
	Site          string
	TrustTier     uint8
	ClientIP      netip.Addr
	IPSource      string
	Country       string
	ASN           uint32
	UA            string
	DeviceClass   string
	OSFamily      string
	BrowserFamily string
}

func deriveRequestContext(r *http.Request, asn *asnTable) requestContext {
	ctx := requestContext{
		Site:      siteFromHost(r.Host),
		TrustTier: tierClientClaimed,
		UA:        truncate(r.UserAgent(), 512),
		ClientIP:  netip.IPv6Unspecified(),
	}
	ctx.DeviceClass, ctx.OSFamily, ctx.BrowserFamily = classifyUA(ctx.UA)
	source := r.Header.Get(headerClientIPSource)
	rawIP := r.Header.Get(headerClientIP)
	if source == "cloudflare" && rawIP != "" {
		if addr, err := netip.ParseAddr(rawIP); err == nil {
			ctx.ClientIP = mapToV6(addr)
			ctx.IPSource = source
			ctx.TrustTier = tierEdgeVerified
			// Relayed by the same verify-gated ingress map as the IP:
			// ISO 3166-1 alpha-2 plus Cloudflare's XX (unknown) / T1 (Tor).
			ctx.Country = truncate(r.Header.Get(headerClientCountry), 8)
			ctx.ASN = asn.lookup(ctx.ClientIP)
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
	if h == "rumi.engineering" || strings.HasSuffix(h, ".rumi.engineering") {
		return "rumi.engineering"
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
