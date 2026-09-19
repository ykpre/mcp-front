package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"strings"

	"github.com/stainless-api/mcp-front/internal/config"
	"github.com/stainless-api/mcp-front/internal/jsonrpc"
	"github.com/stainless-api/mcp-front/internal/log"
)

// forwardStreamablePostToBackend handles POST requests for streamable-http transport
func forwardStreamablePostToBackend(ctx context.Context, w http.ResponseWriter, r *http.Request, config *config.MCPClientConfig) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		log.LogErrorWithFields("streamable_proxy", "Failed to read request body", map[string]any{
			"error": err.Error(),
		})
		jsonrpc.WriteError(w, nil, jsonrpc.InternalError, "Failed to read request")
		return
	}

	// SEP-2575 subscriptions/listen is a request the server answers by holding
	// the response stream open for the rest of the session. Like the standalone
	// GET stream, that keeps a backend instance billed around the clock per
	// client session, so it is refused here before it reaches any backend.
	// Clients treat the error as "listen unavailable" and fall back to plain
	// polling; none of the proxied backends change their tool list anyway.
	if rpc := parseJSONRPCRequest(body); rpc != nil && rpc.Method == "subscriptions/listen" {
		log.LogInfoWithFields("streamable_proxy", "Rejecting subscriptions/listen request", map[string]any{
			"backendURL": config.URL,
			"userAgent":  r.UserAgent(),
		})
		jsonrpc.WriteError(w, rpc.ID, jsonrpc.MethodNotFound, "subscriptions/listen not offered")
		return
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, config.URL, bytes.NewReader(body))
	if err != nil {
		log.LogErrorWithFields("streamable_proxy", "Failed to create backend request", map[string]any{
			"error": err.Error(),
		})
		jsonrpc.WriteError(w, nil, jsonrpc.InternalError, "Failed to create request")
		return
	}

	// Copy relevant headers from original request, excluding hop-by-hop and sensitive headers
	copyRequestHeaders(req.Header, r.Header)

	// Add configured headers (e.g., auth headers)
	for k, v := range config.Headers {
		req.Header.Set(k, v)
	}

	if config.GoogleIDTokenAudience != "" {
		token, err := mintIDToken(config.GoogleIDTokenAudience)
		if err != nil {
			log.LogErrorWithFields("streamable_proxy", "Failed to mint ID token", map[string]any{
				"error":    err.Error(),
				"audience": config.GoogleIDTokenAudience,
			})
			jsonrpc.WriteError(w, nil, jsonrpc.InternalError, "backend authentication failed")
			return
		}
		req.Header.Set(config.IDTokenHeader(), "Bearer "+token)
	}

	req.Header.Set("Accept", "application/json, text/event-stream")

	log.LogDebugWithFields("streamable_proxy", "Forwarding POST to backend", map[string]any{
		"backendURL": config.URL,
		"method":     r.Method,
		"headers":    config.Headers,
	})

	client := &http.Client{
		Timeout: config.Timeout,
	}
	resp, err := client.Do(req)
	if err != nil {
		log.LogErrorWithFields("streamable_proxy", "Backend request failed", map[string]any{
			"error": err.Error(),
			"url":   config.URL,
		})
		jsonrpc.WriteError(w, nil, jsonrpc.InternalError, "backend request failed")
		return
	}
	defer resp.Body.Close()

	contentType := resp.Header.Get("Content-Type")

	if strings.HasPrefix(contentType, "text/event-stream") {
		log.LogInfoWithFields("streamable_proxy", "Backend returned SSE stream", map[string]any{
			"status": resp.StatusCode,
		})

		for k, v := range resp.Header {
			if k == "Content-Type" || k == "Cache-Control" || k == "Connection" || k == "Mcp-Session-Id" {
				w.Header()[k] = v
			}
		}

		w.WriteHeader(resp.StatusCode)

		flusher, ok := w.(http.Flusher)
		if !ok {
			log.LogError("Response writer doesn't support flushing")
			return
		}

		streamSSEResponse(w, flusher, resp.Body, "streamable_proxy")
	} else {
		maps.Copy(w.Header(), resp.Header)

		w.WriteHeader(resp.StatusCode)

		if _, err := io.Copy(w, resp.Body); err != nil {
			log.LogErrorWithFields("streamable_proxy", "Failed to copy response body", map[string]any{
				"error": err.Error(),
			})
		}
	}
}

// parseJSONRPCRequest returns the request when body is a single JSON-RPC
// request object, or nil for anything else (batches, notifications, garbage).
func parseJSONRPCRequest(body []byte) *jsonrpc.Request {
	var rpc jsonrpc.Request
	if err := json.Unmarshal(body, &rpc); err != nil || rpc.Method == "" {
		return nil
	}
	return &rpc
}
