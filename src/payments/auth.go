package main

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/coreos/go-oidc/v3/oidc"

	authorizationv1 "github.com/guardian-intelligence/guardian/src/proto/gen/go/guardian/authorization/v1"
	"github.com/guardian-intelligence/guardian/src/proto/gen/go/guardian/authorization/v1/authorizationv1connect"
)

type customerIdentity struct {
	Subject string
}

type tokenVerifier interface {
	Verify(context.Context, string) (customerIdentity, error)
}

type oidcVerifier struct {
	verifier *oidc.IDTokenVerifier
}

func newOIDCVerifier(ctx context.Context, issuer, clientID string) (*oidcVerifier, error) {
	provider, err := oidc.NewProvider(ctx, issuer)
	if err != nil {
		return nil, err
	}
	return &oidcVerifier{verifier: provider.Verifier(&oidc.Config{ClientID: clientID})}, nil
}

func (v *oidcVerifier) Verify(ctx context.Context, raw string) (customerIdentity, error) {
	token, err := v.verifier.Verify(ctx, raw)
	if err != nil {
		return customerIdentity{}, err
	}
	var claims struct {
		Subject string `json:"sub"`
	}
	if err := token.Claims(&claims); err != nil {
		return customerIdentity{}, err
	}
	if claims.Subject == "" {
		return customerIdentity{}, errors.New("token lacks subject claim")
	}
	return customerIdentity{Subject: claims.Subject}, nil
}

type authorizationChecker interface {
	CheckOrganization(context.Context, string, string, string) (bool, error)
}

type connectAuthorizationChecker struct {
	client authorizationv1connect.AuthorizationServiceClient
	token  string
}

func newAuthorizationChecker(baseURL, token string) *connectAuthorizationChecker {
	return &connectAuthorizationChecker{
		client: authorizationv1connect.NewAuthorizationServiceClient(
			&http.Client{Timeout: 5 * time.Second},
			baseURL,
		),
		token:  token,
	}
}

func (c *connectAuthorizationChecker) CheckOrganization(
	ctx context.Context,
	subject string,
	organizationID string,
	permission string,
) (bool, error) {
	request := connect.NewRequest(&authorizationv1.CheckRequest{
		Resource: &authorizationv1.ObjectReference{
			Type: "organization",
			Id:   organizationID,
		},
		Permission: permission,
		Subject: &authorizationv1.SubjectReference{
			Object: &authorizationv1.ObjectReference{
				Type: "guardian_account",
				Id:   subject,
			},
		},
	})
	request.Header().Set("Authorization", "Bearer "+c.token)
	response, err := c.client.Check(ctx, request)
	if err != nil {
		return false, err
	}
	return response.Msg.GetAllowed(), nil
}

func bearerToken(r *http.Request) (string, error) {
	scheme, token, ok := strings.Cut(strings.TrimSpace(r.Header.Get("Authorization")), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") || strings.TrimSpace(token) == "" {
		return "", errors.New("missing bearer token")
	}
	return strings.TrimSpace(token), nil
}
