// Package connectpolicy is the central Connect interceptor that reads each
// method's declared OperationPolicy (guardian/api/v1/policy.proto) and
// enforces it fail-closed: a served method with no policy is denied, and a
// method that declares a required_permission is denied until an
// authenticator is wired. Rate-limit class and audit level are declared here
// but owned/enforced by their layers (edge limiter, audit sink); this
// interceptor emits the audit record and surfaces the class.
package connectpolicy

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"

	apiv1 "github.com/guardian-intelligence/guardian/src/proto/gen/go/guardian/api/v1"
)

// Authenticator turns request headers into a set of granted permissions. Nil
// until an authenticated service wires one (Keycloak/PDP); methods that
// declare a required_permission are denied while it is nil.
type Authenticator interface {
	Permissions(ctx context.Context, header http.Header) (map[string]struct{}, error)
}

// NewInterceptor returns the policy interceptor. auth may be nil.
func NewInterceptor(auth Authenticator) connect.UnaryInterceptorFunc {
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			if req.Spec().IsClient {
				return next(ctx, req)
			}
			pol, ok := methodPolicy(req.Spec().Procedure)
			if !ok {
				return nil, connect.NewError(connect.CodePermissionDenied, errUnpoliced)
			}
			if perm := pol.GetRequiredPermission(); perm != "" {
				if auth == nil {
					return nil, connect.NewError(connect.CodeUnauthenticated, errNoAuthenticator)
				}
				granted, err := auth.Permissions(ctx, req.Header())
				if err != nil {
					return nil, connect.NewError(connect.CodeUnauthenticated, err)
				}
				if _, held := granted[perm]; !held {
					return nil, connect.NewError(connect.CodePermissionDenied, errMissingPermission)
				}
			}

			res, err := next(ctx, req)
			if pol.GetAudit() >= apiv1.AuditLevel_AUDIT_LEVEL_METADATA {
				slog.Info("rpc",
					"procedure", req.Spec().Procedure,
					"rate_limit_class", pol.GetRateLimitClass(),
					"code", connect.CodeOf(err),
				)
			}
			return res, err
		}
	}
}

// methodPolicy resolves "/pkg.Service/Method" to its declared OperationPolicy.
func methodPolicy(procedure string) (*apiv1.OperationPolicy, bool) {
	name := protoreflect.FullName(strings.Replace(strings.TrimPrefix(procedure, "/"), "/", ".", 1))
	desc, err := protoregistry.GlobalFiles.FindDescriptorByName(name)
	if err != nil {
		return nil, false
	}
	md, ok := desc.(protoreflect.MethodDescriptor)
	if !ok {
		return nil, false
	}
	opts := md.Options()
	if opts == nil || !proto.HasExtension(opts, apiv1.E_Policy) {
		return nil, false
	}
	pol, ok := proto.GetExtension(opts, apiv1.E_Policy).(*apiv1.OperationPolicy)
	if !ok || pol == nil {
		return nil, false
	}
	return pol, true
}

type constError string

func (e constError) Error() string { return string(e) }

const (
	errUnpoliced         = constError("method declares no operation policy")
	errNoAuthenticator   = constError("method requires a permission but no authenticator is configured")
	errMissingPermission = constError("caller lacks the required permission")
)
