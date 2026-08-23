// Package googleidtoken mints Google-signed ID tokens from the ambient
// service account credentials (metadata server on GCP, ADC elsewhere).
// Used to call backends protected by Cloud Run IAM.
package googleidtoken

import (
	"context"
	"fmt"
	"net/http"
	"sync"

	"golang.org/x/oauth2"
	"google.golang.org/api/idtoken"
)

var (
	mu      sync.Mutex
	sources = map[string]oauth2.TokenSource{}
)

// source returns a cached token source for the audience. Sources refresh
// tokens internally, so lookups after the first are cheap.
func source(audience string) (oauth2.TokenSource, error) {
	mu.Lock()
	defer mu.Unlock()
	ts, ok := sources[audience]
	if !ok {
		var err error
		// Background context: the source outlives any single request and
		// refreshes tokens for the lifetime of the process.
		ts, err = idtoken.NewTokenSource(context.Background(), audience)
		if err != nil {
			return nil, fmt.Errorf("creating ID token source for %q: %w", audience, err)
		}
		sources[audience] = ts
	}
	return ts, nil
}

// Bearer returns a valid ID token for the audience.
func Bearer(audience string) (string, error) {
	ts, err := source(audience)
	if err != nil {
		return "", err
	}
	tok, err := ts.Token()
	if err != nil {
		return "", fmt.Errorf("minting ID token for %q: %w", audience, err)
	}
	return tok.AccessToken, nil
}

// HTTPClient returns an http.Client that attaches an ID token for the
// audience to every request in the given header.
func HTTPClient(audience, header string) (*http.Client, error) {
	ts, err := source(audience)
	if err != nil {
		return nil, err
	}
	return &http.Client{Transport: &tokenTransport{ts: ts, header: header, base: http.DefaultTransport}}, nil
}

type tokenTransport struct {
	ts     oauth2.TokenSource
	header string
	base   http.RoundTripper
}

func (t *tokenTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	tok, err := t.ts.Token()
	if err != nil {
		return nil, err
	}
	req = req.Clone(req.Context())
	req.Header.Set(t.header, "Bearer "+tok.AccessToken)
	return t.base.RoundTrip(req)
}
