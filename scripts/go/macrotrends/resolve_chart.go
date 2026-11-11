package main

import (
	"fmt"
	"net/url"
	"strings"
)

// epsPageURL is the PE-ratio page. MacroTrends redirects /{TICKER}/{ticker}/pe-ratio
// to /{TICKER}/{company-slug}/pe-ratio, including when the slug is not the ticker.
func epsPageURL(symbol string) string {
	symbol = normalizeSymbol(symbol)
	return "https://www.macrotrends.net/stocks/charts/" + symbol + "/" + strings.ToLower(symbol) + "/pe-ratio"
}

func validateChartCanonicalURL(finalURL, expectedSymbol string) error {
	u, err := url.Parse(finalURL)
	if err != nil {
		return fmt.Errorf("invalid final URL %q: %w", finalURL, err)
	}
	segments := pathSegments(u.Path)
	if len(segments) < 4 ||
		segments[0] != "stocks" ||
		segments[1] != "charts" ||
		strings.ToUpper(segments[2]) != expectedSymbol {
		return fmt.Errorf(
			"MacroTrends did not redirect to a /stocks/charts/{ticker}/{slug} URL; got %q",
			finalURL,
		)
	}
	return nil
}

func pathSegments(path string) []string {
	path = strings.Trim(path, "/")
	if path == "" {
		return nil
	}
	parts := strings.Split(path, "/")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
