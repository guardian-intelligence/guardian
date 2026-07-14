package checkoutbundle

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
)

// tokenScope namespaces checkout tokens away from other lease-scoped tokens
// (runner bootstrap, telemetry) derived from the same host secret.
const tokenScope = "postflight-checkout"

const maxIdentifierLength = 256

var errUnauthorized = errors.New("checkout request is not authorized")

// DeriveCheckoutToken derives the bearer token hostd injects into the runner
// environment as POSTFLIGHT_CHECKOUT_TOKEN. It is purely derivable from the
// host secret and the lease coordinates: nothing is stored, and the token
// stops authenticating the moment the lease stops resolving.
func DeriveCheckoutToken(hostSecret []byte, executionID, attemptID string) string {
	mac := hmac.New(sha256.New, append([]byte(tokenScope+":"), hostSecret...))
	mac.Write([]byte(executionID))
	mac.Write([]byte(":"))
	mac.Write([]byte(attemptID))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
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
		return LeaseIdentity{}, err
	}
	if !ok {
		return LeaseIdentity{}, errUnauthorized
	}
	return identity, nil
}
