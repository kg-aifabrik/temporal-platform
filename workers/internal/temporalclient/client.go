// Package temporalclient builds a Temporal SDK client from environment
// variables, shared by both teams' workers.
//
// The one interesting part is auth: when TEMPORAL_AUTH_TOKEN is set, every gRPC
// call carries an `authorization: Bearer <token>` header. Temporal's frontend
// claim mapper reads that header to decide the caller's roles. When the token is
// empty (the baseline, pre-RBAC setup) no header is attached and the frontend's
// noop authorizer allows everything. The same binary therefore works before and
// after RBAC is switched on — only the environment changes.
package temporalclient

import (
	"context"
	"os"

	"go.temporal.io/sdk/client"
)

// bearerHeaders injects a static bearer token on every request.
type bearerHeaders struct{ token string }

func (b bearerHeaders) GetHeaders(context.Context) (map[string]string, error) {
	if b.token == "" {
		return nil, nil
	}
	return map[string]string{"authorization": "Bearer " + b.token}, nil
}

// New dials Temporal using TEMPORAL_ADDRESS (default 127.0.0.1:7233),
// TEMPORAL_NAMESPACE (default "default"), and optional TEMPORAL_AUTH_TOKEN.
func New() (client.Client, error) {
	opts := client.Options{
		HostPort:  env("TEMPORAL_ADDRESS", "127.0.0.1:7233"),
		Namespace: env("TEMPORAL_NAMESPACE", "default"),
	}
	if tok := os.Getenv("TEMPORAL_AUTH_TOKEN"); tok != "" {
		opts.HeadersProvider = bearerHeaders{token: tok}
	}
	return client.Dial(opts)
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
