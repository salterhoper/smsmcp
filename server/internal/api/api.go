// Package api wires the HTTP routes together.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/salterhoper/smsmcp/server/internal/hub"
	"github.com/salterhoper/smsmcp/server/internal/mcpserver"
	"github.com/salterhoper/smsmcp/server/internal/pairing"
	"github.com/salterhoper/smsmcp/server/internal/store"
)

type Deps struct {
	Store   *store.Store
	Hub     *hub.Hub
	MCP     *mcpserver.Server
	Version string
}

// Routes:
//
//	GET  /healthz          liveness
//	POST /v1/pair          phone pairing (token from QR)
//	GET  /v1/device/ws     phone WebSocket (device token)
//	*    /mcp              MCP Streamable HTTP (API key)
func Router(d Deps) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok", "version": d.Version})
	})
	mux.Handle("POST /v1/pair", pairing.Handler(d.Store))
	mux.Handle("GET /v1/device/ws", d.Hub)

	srv := d.MCP.New()
	mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)
	mux.Handle("/mcp", auth.RequireBearerToken(verifier(d.Store), nil)(mcpHandler))

	return mux
}

// verifier maps an API key to TokenInfo. UserID carries the key id, which
// scopes idempotency keys and pins MCP sessions to the key that opened them.
func verifier(st *store.Store) auth.TokenVerifier {
	return func(ctx context.Context, token string, _ *http.Request) (*auth.TokenInfo, error) {
		k, err := st.APIKeyBySecret(ctx, token)
		if errors.Is(err, store.ErrNotFound) {
			return nil, auth.ErrInvalidToken
		} else if err != nil {
			return nil, err
		}
		return &auth.TokenInfo{UserID: k.ID, Expiration: time.Now().Add(time.Hour)}, nil
	}
}
