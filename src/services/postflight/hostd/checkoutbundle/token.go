package checkoutbundle

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"hash"
	"net/http"
	"strings"
)

// tokenScope namespaces checkout tokens away from other lease-scoped tokens
// (runner bootstrap, telemetry) derived from the same host secret.
const tokenScope = "postflight-checkout"

const maxIdentifierLength = 256

var (
	errUnauthorized = errors.New("checkout request is not authorized")
	// errResolverUnavailable marks a lease-lookup infrastructure failure, as
	// opposed to a genuine "no such active lease". The former is retryable
	// (the client should back off, not fail the job); the latter is terminal.
	errResolverUnavailable = errors.New("lease lookup is unavailable")
)

// DeriveCheckoutToken derives the bearer token hostd injects into the runner
// environment as POSTFLIGHT_CHECKOUT_TOKEN. It is purely derivable from the
// host secret and the lease coordinates: nothing is stored, and the token
// stops authenticating the moment the lease stops resolving.
//
// The identifiers are length-prefixed rather than delimiter-joined so no pair
// of (execution, attempt) values can alias another: a bare delimiter would let
// ("a:b","c") and ("a","b:c") derive the same token.
func DeriveCheckoutToken(hostSecret []byte, executionID, attemptID string) string {
	mac := hmac.New(sha256.New, append([]byte(tokenScope+":"), hostSecret...))
	writeLengthPrefixed(mac, executionID)
	writeLengthPrefixed(mac, attemptID)
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func writeLengthPrefixed(mac hash.Hash, value string) {
	var header [8]byte
	binary.BigEndian.PutUint64(header[:], uint64(len(value)))
	mac.Write(header[:])
	mac.Write([]byte(value))
}

// authenticate verifies the bearer against the derived token for the claimed
// execution/attempt pair and resolves the active lease. Every failure mode
// returns the same error so the response does not disclose which check
// failed.
func (s *Service) authenticate(ctx context.Context, r *http.Request) (LeaseIdentity, error) {
	executionID := strings.TrimSpace(r.Header.Get(executionIDHeader))
	attemptID := strings.TrimSpace(r.Header.Get(attemptIDHeader))
	bearer := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(r.Header.Get("Authorization")), "Bearer "))
	if executionID == "" || attemptID == "" || bearer == "" ||
		len(executionID) > maxIdentifierLength || len(attemptID) > maxIdentifierLength {
		return LeaseIdentity{}, errUnauthorized
	}
	expected := DeriveCheckoutToken(s.cfg.HostSecret, executionID, attemptID)
	if !hmac.Equal([]byte(bearer), []byte(expected)) {
		return LeaseIdentity{}, errUnauthorized
	}
	identity, ok, err := s.resolver.ResolveActiveLease(ctx, executionID, attemptID)
	if err != nil {
		return LeaseIdentity{}, errResolverUnavailable
	}
	if !ok {
		return LeaseIdentity{}, errUnauthorized
	}
	return identity, nil
}
