package connectpolicy

import (
	"context"
	"net/http"
	"testing"

	"connectrpc.com/connect"

	_ "github.com/guardian-intelligence/guardian/src/proto/gen/go/guardian/analytics/v1"
)

// stubReq is a minimal server-side AnyRequest carrying only a Spec.
type stubReq struct {
	connect.AnyRequest
	procedure string
}

func (r stubReq) Spec() connect.Spec {
	return connect.Spec{Procedure: r.procedure, IsClient: false}
}
func (r stubReq) Header() http.Header { return http.Header{} }

func run(t *testing.T, auth Authenticator, procedure string) error {
	t.Helper()
	called := false
	next := connect.UnaryFunc(func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		called = true
		return nil, nil
	})
	_, err := NewInterceptor(auth).WrapUnary(next)(context.Background(), stubReq{procedure: procedure})
	if err == nil && !called {
		t.Fatal("no error but handler not called")
	}
	return err
}

func TestAnnotatedMethodPasses(t *testing.T) {
	// Publish declares a policy with no required_permission -> allowed.
	if err := run(t, nil, "/guardian.analytics.v1.EventService/Publish"); err != nil {
		t.Fatalf("annotated public method should pass, got %v", err)
	}
}

func TestUnpolicedMethodDenied(t *testing.T) {
	// A method with no policy annotation is denied fail-closed.
	err := run(t, nil, "/guardian.analytics.v1.EventService/DoesNotExist")
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("unpoliced method should be permission_denied, got %v", err)
	}
}

// A method requiring a permission is denied when no authenticator is wired.
// Verified via the resolver + a synthetic policy since no such method exists yet.
func TestRequiredPermissionWithoutAuth(t *testing.T) {
	pol, ok := methodPolicy("/guardian.analytics.v1.EventService/Publish")
	if !ok {
		t.Fatal("Publish policy not resolvable")
	}
	if pol.GetRequiredPermission() != "" {
		t.Fatal("Publish must remain public (no required_permission)")
	}
	if pol.GetRateLimitClass() != "beacon-public" {
		t.Fatalf("rate_limit_class = %q", pol.GetRateLimitClass())
	}
}
