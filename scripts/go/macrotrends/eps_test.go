package main

import (
	"context"
	"strings"
	"testing"
)

const chartJSONFixture = `<html><body><table class="mtf-table mtf-peers"><thead><tr><th>Company</th></tr></thead></table>
<script>
window.CHART_CONFIG = {"chartType":"threePanel","seriesData":[{"date":"2020-03-31","v1":27.92,"v2":-6.25,"v3":null},{"date":"2026-11-20","v1":63.475,"v2":1.71,"v3":37.12}],"options":{"title":"PE Ratio","panels":[{"field":"v1","title":"Stock Price"},{"field":"v2","title":"TTM Net EPS"},{"field":"v3","title":"PE Ratio"}]}};
window.CHART_DATA = [];
</script></body></html>`

func TestParseEPSHistoryFromChartJSON(t *testing.T) {
	rows := parseEPSHistoryFromChartJSON(chartJSONFixture)
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}
	// newest-first
	if rows[0].date != "2026-11-20" || rows[1].date != "2020-03-31" {
		t.Fatalf("unexpected order: %s, %s", rows[0].date, rows[1].date)
	}
	if rows[0].stockPrice == nil || *rows[0].stockPrice != 63.475 {
		t.Errorf("stock price = %v", rows[0].stockPrice)
	}
	if rows[0].ttmNetEPS == nil || *rows[0].ttmNetEPS != 1.71 {
		t.Errorf("eps = %v", rows[0].ttmNetEPS)
	}
	if rows[0].peRatio == nil || *rows[0].peRatio != 37.12 {
		t.Errorf("pe = %v", rows[0].peRatio)
	}
	if rows[1].ttmNetEPS == nil || *rows[1].ttmNetEPS != -6.25 {
		t.Errorf("negative eps = %v", rows[1].ttmNetEPS)
	}
	if rows[1].peRatio != nil {
		t.Errorf("null pe should be nil, got %v", *rows[1].peRatio)
	}
}

func TestFetchEPSHistoryRetriesTruncatedPage(t *testing.T) {
	const truncated = `<html><head><title>Uber Technologies PE Ratio | UBER | MacroTrends</title></head><body></body></html>`
	const finalURL = "https://www.macrotrends.net/stocks/charts/UBER/uber-technologies/pe-ratio"

	calls := 0
	var waits []string
	get := func(_ context.Context, _, waitFor string) ([]byte, string, error) {
		calls++
		waits = append(waits, waitFor)
		if calls == 1 {
			return []byte(truncated), finalURL, nil
		}
		return []byte(chartJSONFixture), finalURL, nil
	}
	rows, err := fetchEPSHistoryWith(context.Background(), "UBER", get, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || calls != 2 {
		t.Fatalf("rows=%d calls=%d, want 2 rows after 2 calls", len(rows), calls)
	}
	// only the retry waits for the page content
	if waits[0] != "" || waits[1] != epsPageReadySelector {
		t.Errorf("wait_for per attempt = %q", waits)
	}

	calls = 0
	alwaysTruncated := func(context.Context, string, string) ([]byte, string, error) {
		calls++
		return []byte(truncated), finalURL, nil
	}
	_, err = fetchEPSHistoryWith(context.Background(), "UBER", alwaysTruncated, 0)
	if err == nil || calls != epsFetchAttempts {
		t.Fatalf("err=%v calls=%d, want an error after %d calls", err, calls, epsFetchAttempts)
	}
	if !strings.Contains(err.Error(), "Uber Technologies PE Ratio") {
		t.Errorf("error should name the page title: %v", err)
	}
}

func TestParseEPSHistoryFromChartJSONMissing(t *testing.T) {
	if rows := parseEPSHistoryFromChartJSON("<html></html>"); len(rows) != 0 {
		t.Fatalf("expected no rows, got %d", len(rows))
	}
}
