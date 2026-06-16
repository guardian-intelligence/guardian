package main

import (
	"context"
	"net/http"

	"connectrpc.com/connect"
	aisucksv1 "github.com/guardian-intelligence/guardian/src/products/aisucks/api/guardian/products/aisucks/v1"
)

var healthCapabilities = []string{
	"health.connect.v1",
	"health.capabilities.v1",
}

type healthRPC struct {
	version string
}

func newHealthRPCHandler(version string) (string, http.Handler) {
	return aisucksv1.NewAisucksServiceHandler(&healthRPC{version: version})
}

func (h *healthRPC) Health(
	context.Context,
	*connect.Request[aisucksv1.HealthRequest],
) (*connect.Response[aisucksv1.HealthResponse], error) {
	return connect.NewResponse(&aisucksv1.HealthResponse{
		Status:       "ok",
		Service:      "aisucks-api",
		Version:      h.version,
		Capabilities: append([]string(nil), healthCapabilities...),
	}), nil
}
