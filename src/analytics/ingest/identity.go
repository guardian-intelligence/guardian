package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
)

// Visitor identity rides the guardian_correlation_id cookie — the same
// cookie the web app's server plugin mints for request logs, so analytics
// rows and SSR logs join on one id. The ingest service mints one itself
// when absent (first-ever hit lands here before any SSR response) with
// attributes identical to the web plugin's. HMAC signing of the cookie is
// deferred to the OpenBao scope for this namespace; until then the value is
// an unauthenticated UUID, which a client can only abuse to fragment or
// pollute its own identity clustering.

const correlationCookie = "guardian_correlation_id"

// correlationID returns the 16 raw bytes of the visitor id plus the cookie
// to set when a new id was minted (nil when the request already carried a
// usable one).
func correlationID(r *http.Request) ([16]byte, *http.Cookie) {
	var id [16]byte
	if c, err := r.Cookie(correlationCookie); err == nil {
		if b, ok := parseUUID(c.Value); ok {
			return b, nil
		}
	}
	if _, err := rand.Read(id[:]); err != nil {
		// Zero id groups unattributable events; never fail publish on it.
		return id, nil
	}
	// RFC 4122 v4 bits so the minted value round-trips as a normal UUID.
	id[6] = (id[6] & 0x0f) | 0x40
	id[8] = (id[8] & 0x3f) | 0x80
	u := fmt.Sprintf("%x-%x-%x-%x-%x", id[0:4], id[4:6], id[6:8], id[8:10], id[10:16])
	return id, &http.Cookie{
		Name:     correlationCookie,
		Value:    u,
		Path:     "/",
		MaxAge:   3600 * 24 * 30,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	}
}

func parseUUID(s string) ([16]byte, bool) {
	var out [16]byte
	s = strings.ReplaceAll(strings.TrimSpace(s), "-", "")
	if len(s) != 32 {
		return out, false
	}
	b, err := hex.DecodeString(s)
	if err != nil {
		return out, false
	}
	copy(out[:], b)
	return out, true
}
