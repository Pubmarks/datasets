package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func mkRow(date string, price, eps, pe *float64) epsRow {
	return epsRow{date: date, stockPrice: price, ttmNetEPS: eps, peRatio: pe}
}

func f(v float64) *float64 { return &v }

// closes around the AAPL quarter ends used below (real values)
func testCloses() *closeSeries {
	return &closeSeries{
		dates:  []string{"2006-12-28", "2006-12-29", "2007-03-30", "2012-09-28", "2012-12-31", "2013-03-28", "2013-06-28", "2013-09-30", "2013-12-31"},
		closes: []float64{2.89, 3.03, 3.318, 23.825, 19.006, 15.809, 14.162, 17.027, 20.036},
	}
}

// two legacy 2-decimal rows on the old (dividend-adjusted) basis, then rows as
// MacroTrends serves them today, then the latest-day row
func testRows() []epsRow {
	return []epsRow{
		mkRow("2006-12-31", f(2.54), f(0.1), f(25.74)),
		mkRow("2007-03-31", f(2.78), f(0.11), f(24.61)),
		mkRow("2012-09-30", f(23.8252), f(1.5776), f(15.1)),
		mkRow("2012-12-31", f(19.0062), f(1.5754), f(12.06)),
		mkRow("2013-03-31", f(15.8093), f(1.4965), f(10.56)),
		mkRow("2013-06-30", f(14.1618), f(1.4311), f(9.9)),
		mkRow("2013-09-30", f(17.0268), f(1.42), f(11.99)),
		mkRow("2013-12-31", f(21.5), f(1.4443), f(14.89)),
	}
}

func TestCloseOnOrBefore(t *testing.T) {
	px := testCloses()
	if c, ok := px.closeOnOrBefore("2006-12-31"); !ok || c != 3.03 {
		t.Errorf("weekend quarter end: got %v %v, want 3.03", c, ok)
	}
	if c, ok := px.closeOnOrBefore("2012-12-31"); !ok || c != 19.006 {
		t.Errorf("exact day: got %v %v", c, ok)
	}
	if _, ok := px.closeOnOrBefore("2014-01-02"); ok {
		t.Error("date after the last close must not resolve")
	}
	if _, ok := px.closeOnOrBefore("2006-01-01"); ok {
		t.Error("date before the first close must not resolve")
	}
	if _, ok := px.closeOnOrBefore("2007-06-30"); ok {
		t.Error("gap of more than 7 days must not resolve")
	}
}

func TestRebaseEPSRows(t *testing.T) {
	rows := testRows()
	out, changes, err := rebaseEPSRows(rows, testCloses())
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != len(rows) {
		t.Fatalf("row count changed: %d -> %d", len(rows), len(out))
	}
	for i := range rows {
		if out[i].date != rows[i].date {
			t.Fatalf("row %d date changed", i)
		}
	}
	if len(changes) != 2 {
		t.Fatalf("got %d changes, want 2 (the legacy rows): %+v", len(changes), changes)
	}
	// 2006-12-31: price -> close, pe = 25.74 * 3.03 / 2.54, eps = 2.54 / 25.74
	if got := fmtEPSRow(out[0]); got != "3.03,0.0987,30.71" {
		t.Errorf("2006-12-31 = %s", got)
	}
	if got := fmtEPSRow(out[1]); got != "3.318,0.113,29.37" {
		t.Errorf("2007-03-31 = %s", got)
	}
	// rows already matching the close, and the newest row, are untouched
	for i := 2; i < len(rows); i++ {
		if !epsRowEqual(out[i], rows[i]) {
			t.Errorf("row %s changed: %s", rows[i].date, fmtEPSRow(out[i]))
		}
	}
	// input is not mutated
	if *rows[0].stockPrice != 2.54 {
		t.Error("input rows were mutated")
	}

	again, changes, err := rebaseEPSRows(out, testCloses())
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 0 {
		t.Errorf("second run changed %d row(s): %+v", len(changes), changes)
	}
	for i := range out {
		if !epsRowEqual(again[i], out[i]) {
			t.Errorf("second run changed row %s", out[i].date)
		}
	}
}

func TestRebaseKeepsExactTwoDecimalEPS(t *testing.T) {
	// no EPS in the file has more than 2 decimals, so 2-decimal EPS is exact
	rows := testRows()
	for i := range rows {
		rows[i].ttmNetEPS = f(roundTo(*rows[i].ttmNetEPS, 2))
	}
	out, _, err := rebaseEPSRows(rows, testCloses())
	if err != nil {
		t.Fatal(err)
	}
	if got := fmtEPSRow(out[0]); got != "3.03,0.1,30.71" {
		t.Errorf("2006-12-31 = %s", got)
	}
}

func TestRebaseEdgeRows(t *testing.T) {
	rows := testRows()
	rows[0] = mkRow("2006-12-31", f(2.54), f(-0.1), nil) // loss: no P/E
	rows[1] = mkRow("2007-06-30", f(3.65), f(0.13), f(28.86))
	out, _, err := rebaseEPSRows(rows, testCloses())
	if err != nil {
		t.Fatal(err)
	}
	if got := fmtEPSRow(out[0]); got != "3.03,-0.1," {
		t.Errorf("negative EPS row = %s", got)
	}
	// no close within 7 days: price and P/E stay; EPS is still recovered from them
	if got := fmtEPSRow(out[1]); got != "3.65,0.1265,28.86" {
		t.Errorf("uncovered row = %s", got)
	}
}

func TestRebaseBasisMismatch(t *testing.T) {
	rows := testRows()
	// file still on the pre-split basis: recent prices are 4x the closes
	for i := 2; i < len(rows); i++ {
		rows[i].stockPrice = f(*rows[i].stockPrice * 4)
	}
	if _, _, err := rebaseEPSRows(rows, testCloses()); !errors.Is(err, errBasisMismatch) {
		t.Fatalf("err = %v, want errBasisMismatch", err)
	}
	if _, _, err := rebaseEPSRows(testRows(), &closeSeries{}); !errors.Is(err, errBasisMismatch) {
		t.Fatalf("empty ohlcv: err = %v, want errBasisMismatch", err)
	}
}

func TestDetectSplitFactor(t *testing.T) {
	fetched := []epsRow{
		mkRow("2024-03-31", f(25), f(1.5), f(16.67)),
		mkRow("2024-06-30", f(26), f(1.6), f(16.25)),
		mkRow("2024-09-30", f(27), f(1.7), f(15.88)),
		mkRow("2024-12-31", f(28), f(1.8), f(15.56)),
		mkRow("2025-03-31", f(29), f(1.9), f(15.26)),
	}
	scaled := func(k float64) []epsRow {
		out := make([]epsRow, len(fetched))
		for i, r := range fetched {
			out[i] = mkRow(r.date, f(*r.stockPrice*k), f(roundTo(*r.ttmNetEPS*k, 2)), r.peRatio)
		}
		return out
	}
	if got := detectSplitFactor(scaled(4), fetched); got != 4 {
		t.Errorf("4-for-1: got %v", got)
	}
	if got := detectSplitFactor(scaled(1.5), fetched); got != 1.5 {
		t.Errorf("3-for-2: got %v", got)
	}
	if got := detectSplitFactor(scaled(0.1), fetched); got != 0.1 {
		t.Errorf("1-for-10 reverse: got %v", got)
	}
	if got := detectSplitFactor(scaled(1), fetched); got != 1 {
		t.Errorf("no split: got %v", got)
	}
	if got := detectSplitFactor(scaled(4)[:3], fetched); got != 1 {
		t.Errorf("too little overlap: got %v", got)
	}
	mixed := scaled(1)
	mixed[0].ttmNetEPS, mixed[1].ttmNetEPS, mixed[2].ttmNetEPS = f(6), f(3.2), f(10.2)
	if got := detectSplitFactor(mixed, fetched); got != 1 {
		t.Errorf("inconsistent ratios: got %v", got)
	}
}

func TestMergeEPSWithExistingSplit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "eps.csv")
	// existing file on the pre-split basis; 2023-12-31 is older than the fetch
	existing := `date,stock_price,ttm_net_eps,pe_ratio
2023-12-31,80,4.8,16.67
2024-03-31,100,6,16.67
2024-06-30,104,6.4,16.25
2024-09-30,108,6.8,15.88
2024-12-31,112,7.2,15.56
`
	if err := os.WriteFile(path, []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}
	fetched := []epsRow{
		mkRow("2024-03-31", f(25), f(1.5), f(16.67)),
		mkRow("2024-06-30", f(26), f(1.6), f(16.25)),
		mkRow("2024-09-30", f(27), f(1.7), f(15.88)),
		mkRow("2024-12-31", f(28), f(1.8), f(15.56)),
		mkRow("2025-03-31", f(29), f(1.9), f(15.26)),
	}
	out, factor, err := mergeEPSWithExisting(fetched, path)
	if err != nil {
		t.Fatal(err)
	}
	if factor != 4 {
		t.Errorf("factor = %v, want 4", factor)
	}
	if len(out) != 6 || out[0].date != "2023-12-31" {
		t.Fatalf("preserved row missing: %d rows, first %s", len(out), out[0].date)
	}
	if got := fmtEPSRow(out[0]); got != "20,1.2,16.67" {
		t.Errorf("preserved row = %s, want price and EPS divided by 4, P/E unchanged", got)
	}
}
