package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/coreos/go-oidc/v3/oidc"
)

type customerIdentity struct {
	Subject  string
	TenantID string
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
		Subject      string                         `json:"sub"`
		Organization map[string]organizationDetails `json:"organization"`
	}
	if err := token.Claims(&claims); err != nil {
		return customerIdentity{}, err
	}
	tenantID, err := organizationID(claims.Organization)
	if err != nil {
		return customerIdentity{}, err
	}
	if claims.Subject == "" {
		return customerIdentity{}, errors.New("token lacks subject claim")
	}
	return customerIdentity{Subject: claims.Subject, TenantID: tenantID}, nil
}

type organizationDetails struct {
	ID string `json:"id"`
}

func organizationID(organizations map[string]organizationDetails) (string, error) {
	if len(organizations) != 1 {
		return "", fmt.Errorf("token must contain exactly one organization, got %d", len(organizations))
	}
	for _, organization := range organizations {
		if strings.TrimSpace(organization.ID) == "" {
			return "", errors.New("organization claim lacks id")
		}
		return organization.ID, nil
	}
	panic("unreachable")
}

func bearerToken(r *http.Request) (string, error) {
	scheme, token, ok := strings.Cut(strings.TrimSpace(r.Header.Get("Authorization")), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") || strings.TrimSpace(token) == "" {
		return "", errors.New("missing bearer token")
	}
	return strings.TrimSpace(token), nil
}
