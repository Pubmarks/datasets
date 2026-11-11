package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	zenrowsEndpoint = "https://api.zenrows.com/v1/"
	zenrowsTimeout  = 90 * time.Second
)

// zenrowsGet fetches a MacroTrends URL through ZenRows adaptive mode.
// ZENROWS_API_KEY comes from the environment (a GitHub Actions secret in CI).
// The returned URL is Zr-Final-Url when ZenRows followed a redirect.
func zenrowsGet(ctx context.Context, target string) ([]byte, string, error) {
	apiKey := strings.TrimSpace(os.Getenv("ZENROWS_API_KEY"))
	if apiKey == "" {
		return nil, "", errors.New("ZENROWS_API_KEY is not set")
	}
	q := url.Values{}
	q.Set("apikey", apiKey)
	q.Set("url", target)
	q.Set("mode", "auto")

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, zenrowsEndpoint+"?"+q.Encode(), nil)
	if err != nil {
		return nil, "", err
	}
	client := &http.Client{Timeout: zenrowsTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", redactSecret(err, apiKey)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet := strings.TrimSpace(string(body))
		if len(snippet) > 300 {
			snippet = snippet[:300]
		}
		return nil, "", redactSecret(fmt.Errorf("zenrows GET %s: %s: %s", target, resp.Status, snippet), apiKey)
	}
	final := strings.TrimSpace(resp.Header.Get("Zr-Final-Url"))
	if final == "" {
		final = target
	}
	return body, final, nil
}

func redactSecret(err error, secret string) error {
	if err == nil || secret == "" {
		return err
	}
	return errors.New(strings.ReplaceAll(err.Error(), secret, "REDACTED"))
}
