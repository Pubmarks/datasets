package main

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
)

// epsHistoryTableHeaders must match MacroTrends' PE history <thead> (order-sensitive).
var epsHistoryTableHeaders = []string{"Date", "Stock Price", "TTM Net EPS", "PE Ratio"}

const epsCSVHeader = "date,stock_price,ttm_net_eps,pe_ratio"

type epsRow struct {
	date       string
	stockPrice *float64
	ttmNetEPS  *float64
	peRatio    *float64
}

const (
	epsFetchAttempts   = 3
	epsFetchRetryDelay = 3 * time.Second
)

// epsPageReadySelector is the chart element that follows the embedded history
// in the PE-ratio page; once it exists the history script has been parsed.
const epsPageReadySelector = "#chartContainer"

type pageGetter func(ctx context.Context, target, waitFor string) (body []byte, finalURL string, err error)

func fetchEPSHistory(ctx context.Context, symbol string) ([]epsRow, error) {
	return fetchEPSHistoryWith(ctx, symbol, zenrowsGetWaitFor, epsFetchRetryDelay)
}

// fetchEPSHistoryWith retries when a page arrives without the history. ZenRows
// sometimes snapshots the page before it has loaded (only <head>, a few KB), so
// retries ask it to wait for epsPageReadySelector.
func fetchEPSHistoryWith(ctx context.Context, symbol string, get pageGetter, retryDelay time.Duration) ([]epsRow, error) {
	symbol = normalizeSymbol(symbol)
	pageURL := epsPageURL(symbol)
	var lastErr error
	for attempt := 1; attempt <= epsFetchAttempts; attempt++ {
		waitFor := ""
		if attempt > 1 {
			fmt.Fprintf(os.Stderr, "%v; retrying\n", lastErr)
			waitFor = epsPageReadySelector
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(retryDelay * time.Duration(attempt-1)):
			}
		}
		body, finalURL, err := get(ctx, pageURL, waitFor)
		if err != nil {
			return nil, err
		}
		if err := validateChartCanonicalURL(finalURL, symbol); err != nil {
			return nil, err
		}
		rows := parseEPSHistoryFromHTML(string(body))
		if len(rows) == 0 {
			// MacroTrends' redesigned page embeds the history as JSON in a script
			// instead of an HTML table.
			rows = parseEPSHistoryFromChartJSON(string(body))
		}
		if len(rows) > 0 {
			return rows, nil
		}
		lastErr = fmt.Errorf("no EPS history found in %d-byte page titled %q on attempt %d of %d (expected table thead %v or window.CHART_CONFIG series)",
			len(body), htmlTitle(string(body)), attempt, epsFetchAttempts, epsHistoryTableHeaders)
	}
	return nil, lastErr
}

// htmlTitle returns the page <title>, to tell a block or error page from a layout change.
func htmlTitle(html string) string {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		return ""
	}
	title := strings.TrimSpace(doc.Find("title").First().Text())
	if len(title) > 120 {
		title = title[:120]
	}
	return title
}

// chartConfig is the subset of window.CHART_CONFIG we need. Series values are
// keyed v1, v2, ...; options.panels maps each key to a human title.
type chartConfig struct {
	SeriesData []map[string]any `json:"seriesData"`
	Options    struct {
		Panels []struct {
			Field string `json:"field"`
			Title string `json:"title"`
		} `json:"panels"`
	} `json:"options"`
}

// decodeJSSeriesAssignment decodes the JSON value assigned to `window.<name> = ` in html.
func decodeJSSeriesAssignment(html, name string, v any) bool {
	marker := "window." + name + " = "
	i := strings.Index(html, marker)
	if i < 0 {
		return false
	}
	dec := json.NewDecoder(strings.NewReader(html[i+len(marker):]))
	return dec.Decode(v) == nil
}

// parseEPSHistoryFromChartJSON reads the history from window.CHART_CONFIG
// (seriesData + panel titles). Rows are returned newest-first, matching the
// order of the former HTML table.
func parseEPSHistoryFromChartJSON(html string) []epsRow {
	var cfg chartConfig
	if !decodeJSSeriesAssignment(html, "CHART_CONFIG", &cfg) {
		return nil
	}
	fields := map[string]string{} // panel title -> series key
	for _, p := range cfg.Options.Panels {
		fields[p.Title] = p.Field
	}
	priceKey, okP := fields["Stock Price"]
	epsKey, okE := fields["TTM Net EPS"]
	peKey, okR := fields["PE Ratio"]
	if !okP || !okE || !okR {
		return nil
	}
	num := func(m map[string]any, key string) *float64 {
		f, ok := m[key].(float64)
		if !ok {
			return nil // null or missing
		}
		// the JSON carries float noise (e.g. 1.5776000000000001); trim it
		return ptrFloat(math.Round(f*1e4) / 1e4)
	}
	rows := make([]epsRow, 0, len(cfg.SeriesData))
	for _, pt := range cfg.SeriesData {
		date, _ := pt["date"].(string)
		if date == "" {
			continue
		}
		rows = append(rows, epsRow{
			date:       date,
			stockPrice: num(pt, priceKey),
			ttmNetEPS:  num(pt, epsKey),
			peRatio:    num(pt, peKey),
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].date > rows[j].date })
	return rows
}

// parseEPSHistoryFromHTML finds the first table.table whose thead matches MacroTrends EPS headers.
func parseEPSHistoryFromHTML(html string) []epsRow {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		return nil
	}
	var out []epsRow
	done := false
	doc.Find("table.table").Each(func(_ int, table *goquery.Selection) {
		if done {
			return
		}
		theads := table.Find("thead")
		if theads.Length() == 0 {
			return
		}
		lastThead := theads.Eq(theads.Length() - 1)
		var headers []string
		lastThead.Find("th").Each(func(_ int, th *goquery.Selection) {
			headers = append(headers, strings.TrimSpace(th.Text()))
		})
		if !stringSliceEqual(headers, epsHistoryTableHeaders) {
			return
		}
		tbody := table.Find("tbody").First()
		if tbody.Length() == 0 {
			return
		}
		var rows []epsRow
		tbody.Find("tr").Each(func(_ int, tr *goquery.Selection) {
			var cells []string
			tr.Find("td").Each(func(_ int, td *goquery.Selection) {
				cells = append(cells, strings.TrimSpace(td.Text()))
			})
			if len(cells) != 4 {
				return
			}
			row := epsRow{date: cells[0]}
			if v, ok := parseNumericEPS(cells[1], false); ok {
				row.stockPrice = ptrFloat(v)
			}
			if v, ok := parseNumericEPS(cells[2], true); ok {
				row.ttmNetEPS = ptrFloat(v)
			}
			if v, ok := parseNumericEPS(cells[3], false); ok {
				row.peRatio = ptrFloat(v)
			}
			rows = append(rows, row)
		})
		out = rows
		done = true
	})
	return out
}

func stringSliceEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func parseNumericEPS(raw string, stripCurrency bool) (float64, bool) {
	text := strings.TrimSpace(raw)
	if text == "" {
		return 0, false
	}
	// accounting notation: ($1.23) or (1.23) → -1.23
	negative := strings.HasPrefix(text, "(") && strings.HasSuffix(text, ")")
	if negative {
		text = text[1 : len(text)-1]
	}
	if stripCurrency {
		text = strings.ReplaceAll(text, "$", "")
		text = strings.ReplaceAll(text, ",", "")
	} else {
		text = strings.ReplaceAll(text, ",", "")
	}
	v, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return 0, false
	}
	if negative {
		v = -v
	}
	return v, true
}

func ptrFloat(v float64) *float64 {
	return &v
}

func filterEPSRowsByRange(rows []epsRow, from, to time.Time) []epsRow {
	out := rows[:0]
	for _, r := range rows {
		d, err := time.Parse("2006-01-02", r.date)
		if err != nil {
			continue
		}
		if d.Before(from) || d.After(to) {
			continue
		}
		out = append(out, r)
	}
	return out
}

func writeEPSCSV(w io.Writer, rows []epsRow) error {
	cw := csv.NewWriter(w)
	if err := cw.Write(strings.Split(epsCSVHeader, ",")); err != nil {
		return err
	}
	for _, r := range rows {
		rec := []string{
			r.date,
			fmtEPSFloatPtr(r.stockPrice),
			fmtEPSFloatPtr(r.ttmNetEPS),
			fmtEPSFloatPtr(r.peRatio),
		}
		if err := cw.Write(rec); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

func fmtEPSFloatPtr(p *float64) string {
	if p == nil {
		return ""
	}
	return strconv.FormatFloat(*p, 'f', -1, 64)
}

func writeEPSCSVAtomic(rows []epsRow, path string) error {
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if err := writeEPSCSV(f, rows); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

// readEPSCSVRows reads an existing eps.csv and returns its rows oldest-first.
// Returns nil (no error) if the file does not exist.
func readEPSCSVRows(path string) ([]epsRow, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	cr := csv.NewReader(f)
	cr.FieldsPerRecord = 4
	var rows []epsRow
	first := true
	for {
		rec, err := cr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if first {
			first = false
			continue
		}
		row := epsRow{date: rec[0]}
		if v, e := strconv.ParseFloat(rec[1], 64); e == nil {
			row.stockPrice = ptrFloat(v)
		}
		if v, e := strconv.ParseFloat(rec[2], 64); e == nil {
			row.ttmNetEPS = ptrFloat(v)
		}
		if v, e := strconv.ParseFloat(rec[3], 64); e == nil {
			row.peRatio = ptrFloat(v)
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// mergeEPSWithExisting prepends any rows from existingPath that predate the
// oldest row in fetched. fetched must already be sorted oldest-first.
// Rows MacroTrends no longer serves (older than its current earliest date) are
// preserved so historical data is never silently lost.
//
// If the overlap shows MacroTrends restated EPS for a stock split, the preserved
// rows are moved to the new basis too. The returned factor is 1 when no split
// was detected.
func mergeEPSWithExisting(fetched []epsRow, existingPath string) ([]epsRow, float64, error) {
	if len(fetched) == 0 {
		return fetched, 1, nil
	}
	existing, err := readEPSCSVRows(existingPath)
	if err != nil {
		return nil, 1, err
	}
	fetchedOldest := fetched[0].date
	var preserved []epsRow
	for _, r := range existing {
		if r.date < fetchedOldest {
			preserved = append(preserved, r)
		}
	}
	if len(preserved) == 0 {
		return fetched, 1, nil
	}
	factor := detectSplitFactor(existing, fetched)
	if factor != 1 {
		scaleRowsForSplit(preserved, factor)
	}
	return append(preserved, fetched...), factor, nil
}

func syncEPSYearFiles(root, ticker string, allRows []epsRow, w io.Writer) error {
	byYear := map[int][]epsRow{}
	for _, r := range allRows {
		y, _ := strconv.Atoi(r.date[:4])
		byYear[y] = append(byYear[y], r)
	}
	years := make([]int, 0, len(byYear))
	for y := range byYear {
		years = append(years, y)
	}
	sort.Ints(years)

	for _, year := range years {
		yearPath := filepath.Join(root, "data", "stocks", ticker, strconv.Itoa(year), "eps.csv")
		if err := os.MkdirAll(filepath.Dir(yearPath), 0755); err != nil {
			return err
		}
		yearRows := byYear[year]
		if err := writeEPSCSVAtomic(yearRows, yearPath); err != nil {
			return err
		}
		fmt.Fprintf(w, "wrote %d row(s) to %s\n", len(yearRows), yearPath)
	}
	return nil
}
