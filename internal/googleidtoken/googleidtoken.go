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

// Bearer returns a valid ID token for the audience. Token sources are cached
// per audience and handle refresh internally, so this is cheap per request.
func Bearer(audience string) (string, error) {
	mu.Lock()
	ts, ok := sources[audience]
	if !ok {
		var err error
		// Background context: the source outlives any single request and
		// refreshes tokens for the lifetime of the process.
		ts, err = idtoken.NewTokenSource(context.Background(), audience)
		if err != nil {
			mu.Unlock()
			return "", fmt.Errorf("creating ID token source for %q: %w", audience, err)
		}
		sources[audience] = ts
	}
	mu.Unlock()

	tok, err := ts.Token()
	if err != nil {
		return "", fmt.Errorf("minting ID token for %q: %w", audience, err)
	}
	return tok.AccessToken, nil
}

// HTTPClient returns an http.Client that attaches an ID token for the
// audience to every request.
func HTTPClient(audience string) (*http.Client, error) {
	return idtoken.NewClient(context.Background(), audience)
}
