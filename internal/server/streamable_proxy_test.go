package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stainless-api/mcp-front/internal/config"
	"github.com/stretchr/testify/assert"
)

func TestForwardStreamablePostToBackend_SSESessionHeader(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Mcp-Session-Id", "sess-42")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{}}\n\n"))
		w.(http.Flusher).Flush()
	}))
	defer backend.Close()

	cfg := &config.MCPClientConfig{
		URL:     backend.URL,
		Timeout: 5 * time.Second,
	}

	req := httptest.NewRequest(http.MethodPost, "/test/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call"}`))
	rec := httptest.NewRecorder()

	forwardStreamablePostToBackend(context.Background(), rec, req, cfg)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "text/event-stream", rec.Header().Get("Content-Type"))
	assert.Equal(t, "sess-42", rec.Header().Get("Mcp-Session-Id"), "Mcp-Session-Id header must be forwarded in SSE responses")
	assert.Contains(t, rec.Body.String(), `"result"`)
}

func TestForwardStreamablePostToBackend_RejectsSubscriptionsListen(t *testing.T) {
	backendHit := false
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backendHit = true
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer backend.Close()

	cfg := &config.MCPClientConfig{
		URL:     backend.URL,
		Timeout: 5 * time.Second,
	}

	req := httptest.NewRequest(http.MethodPost, "/test/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":"listen:1","method":"subscriptions/listen","params":{"notifications":{"toolsListChanged":true}}}`))
	rec := httptest.NewRecorder()

	forwardStreamablePostToBackend(context.Background(), rec, req, cfg)

	assert.False(t, backendHit, "subscriptions/listen must not reach the backend")
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	assert.Contains(t, rec.Body.String(), `"id":"listen:1"`)
	assert.Contains(t, rec.Body.String(), `"code":-32601`)
}

func TestForwardStreamablePostToBackend_GoogleIDToken(t *testing.T) {
	var gotAuth string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
	}))
	defer backend.Close()

	orig := mintIDToken
	mintIDToken = func(audience string) (string, error) {
		assert.Equal(t, "https://backend.example.com", audience)
		return "fake-id-token", nil
	}
	defer func() { mintIDToken = orig }()

	cfg := &config.MCPClientConfig{
		URL:                   backend.URL,
		Timeout:               5 * time.Second,
		GoogleIDTokenAudience: "https://backend.example.com",
	}

	req := httptest.NewRequest(http.MethodPost, "/test/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	// A client-supplied Authorization header must not leak through; the
	// minted token wins.
	req.Header.Set("Authorization", "Bearer client-token")
	rec := httptest.NewRecorder()

	forwardStreamablePostToBackend(context.Background(), rec, req, cfg)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "Bearer fake-id-token", gotAuth)
}

func TestForwardStreamablePostToBackend_GoogleIDTokenDualHeader(t *testing.T) {
	var gotAuth, gotServerless string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotServerless = r.Header.Get("X-Serverless-Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
	}))
	defer backend.Close()

	orig := mintIDToken
	mintIDToken = func(audience string) (string, error) { return "fake-id-token", nil }
	defer func() { mintIDToken = orig }()

	cfg := &config.MCPClientConfig{
		URL:                   backend.URL,
		Timeout:               5 * time.Second,
		Headers:               map[string]string{"Authorization": "Bearer static-app-token"},
		GoogleIDTokenAudience: "https://backend.example.com",
	}

	req := httptest.NewRequest(http.MethodPost, "/test/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	rec := httptest.NewRecorder()

	forwardStreamablePostToBackend(context.Background(), rec, req, cfg)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "Bearer static-app-token", gotAuth, "static app token stays in Authorization")
	assert.Equal(t, "Bearer fake-id-token", gotServerless, "ID token goes to X-Serverless-Authorization")
}
