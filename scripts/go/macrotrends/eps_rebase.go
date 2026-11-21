package main

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	// A stored price this close to the OHLCV close is already on the OHLCV basis.
	rebasePriceTolerance = 0.01
	// Longest run of non-trading days we accept between a row date and its close.
	rebaseMaxGapDays = 7
	// Recent rows further than this from OHLCV mean the two files are on
	// different split bases (dividend adjustments never move one year this far).
	rebaseBasisMismatch = 0.10
	// Number of recent rows (before the newest) used for the basis check.
	rebaseBasisRows = 4
)

var errBasisMismatch = errors.New("recent prices disagree with ohlcv.csv (different split basis?); refresh eps first")

// closeSeries is a ticker's daily closes, sorted by date.
type closeSeries struct {
	dates  []string
	closes []float64
}

// readCloseSeries reads date and close from a data/stocks/TICKER/ohlcv.csv file.
func readCloseSeries(path string) (*closeSeries, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	cr := csv.NewReader(f)
	cr.FieldsPerRecord = -1
	header, err := cr.Read()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	iDate, iClose := -1, -1
	for i, h := range header {
		switch strings.ToLower(strings.TrimSpace(h)) {
		case "date":
			iDate = i
		case "close":
			iClose = i
		}
	}
	if iDate < 0 || iClose < 0 {
		return nil, fmt.Errorf("%s: missing date/close columns", path)
	}
	type point struct {
		date  string
		close float64
	}
	var pts []point
	for {
		rec, err := cr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		if len(rec) <= iDate || len(rec) <= iClose {
			continue
		}
		c, err := strconv.ParseFloat(strings.TrimSpace(rec[iClose]), 64)
		if err != nil || c <= 0 {
			continue
		}
		pts = append(pts, point{strings.TrimSpace(rec[iDate]), c})
	}
	sort.Slice(pts, func(i, j int) bool { return pts[i].date < pts[j].date })
	s := &closeSeries{}
	for _, p := range pts {
		s.dates = append(s.dates, p.date)
		s.closes = append(s.closes, p.close)
	}
	return s, nil
}

// closeOnOrBefore returns the close of the last trading day on or before date.
// It reports false when the series does not reach date, or when the nearest
// trading day is more than rebaseMaxGapDays earlier.
func (s *closeSeries) closeOnOrBefore(date string) (float64, bool) {
	n := len(s.dates)
	if n == 0 || date > s.dates[n-1] {
		return 0, false
	}
	i := sort.Search(n, func(k int) bool { return s.dates[k] > date }) - 1
	if i < 0 {
		return 0, false
	}
	want, err1 := time.Parse("2006-01-02", date)
	got, err2 := time.Parse("2006-01-02", s.dates[i])
	if err1 != nil || err2 != nil || want.Sub(got) > rebaseMaxGapDays*24*time.Hour {
		return 0, false
	}
	return s.closes[i], true
}

type rebaseChange struct {
	date     string
	old, new epsRow
}

// rebaseEPSRows puts eps.csv rows (oldest-first) on the same price basis as the
// OHLCV closes. It never adds, drops or reorders rows. For every row but the
// newest (MacroTrends' latest-day quote):
//
//   - EPS: rows older than the first EPS with more than 2 decimals were stored
//     rounded to 2 decimals. Their EPS is recovered as price / pe, which is
//     tighter, and kept only if it still rounds to the stored value.
//   - Price and P/E: when the stored price differs from the close, the P/E is
//     rescaled by close / price (EPS cancels out) and the price set to the close.
//
// A second run changes nothing.
func rebaseEPSRows(rows []epsRow, px *closeSeries) ([]epsRow, []rebaseChange, error) {
	out := make([]epsRow, len(rows))
	copy(out, rows)
	last := len(out) - 1
	if last < 1 {
		return out, nil, nil
	}

	var devs []float64
	for i := last - 1; i >= 0 && len(devs) < rebaseBasisRows; i-- {
		if out[i].stockPrice == nil || *out[i].stockPrice <= 0 {
			continue
		}
		if c, ok := px.closeOnOrBefore(out[i].date); ok {
			devs = append(devs, math.Abs(c / *out[i].stockPrice - 1))
		}
	}
	if len(devs) == 0 || median(devs) > rebaseBasisMismatch {
		return nil, nil, errBasisMismatch
	}

	// rows before the first high-precision EPS are legacy 2-decimal rows
	legacyEnd := 0
	for i, r := range out {
		if r.ttmNetEPS != nil && hasMoreThan2Decimals(*r.ttmNetEPS) {
			legacyEnd = i
			break
		}
	}

	var changes []rebaseChange
	for i := 0; i < last; i++ {
		old := out[i]
		if old.stockPrice == nil || *old.stockPrice <= 0 {
			continue
		}
		row := old
		price := *old.stockPrice

		if i < legacyEnd && old.ttmNetEPS != nil && old.peRatio != nil && *old.peRatio != 0 &&
			!hasMoreThan2Decimals(*old.ttmNetEPS) {
			derived := roundTo(price / *old.peRatio, 4)
			if math.Abs(derived-*old.ttmNetEPS) <= 0.005+1e-9 {
				row.ttmNetEPS = ptrFloat(derived)
			}
		}

		if c, ok := px.closeOnOrBefore(old.date); ok && math.Abs(c-price) > rebasePriceTolerance {
			if old.peRatio != nil {
				row.peRatio = ptrFloat(roundTo(*old.peRatio*c/price, 2))
			}
			row.stockPrice = ptrFloat(c)
		}

		if !epsRowEqual(old, row) {
			out[i] = row
			changes = append(changes, rebaseChange{date: old.date, old: old, new: row})
		}
	}
	return out, changes, nil
}

// detectSplitFactor compares EPS on dates present in both the existing file and
// a fresh fetch. MacroTrends restates every row after a stock split, so a
// consistent ratio other than 1 is the split factor (4 for a 4-for-1 split).
// It returns 1 when there is no clear, consistent factor.
func detectSplitFactor(existing, fetched []epsRow) float64 {
	fresh := make(map[string]float64, len(fetched))
	for _, r := range fetched {
		if r.ttmNetEPS != nil {
			fresh[r.date] = *r.ttmNetEPS
		}
	}
	var ratios []float64
	for _, r := range existing {
		f, ok := fresh[r.date]
		// tiny EPS values are dominated by rounding noise
		if !ok || r.ttmNetEPS == nil || math.Abs(f) < 0.05 || math.Abs(*r.ttmNetEPS) < 0.05 {
			continue
		}
		if ratio := *r.ttmNetEPS / f; ratio > 0 {
			ratios = append(ratios, ratio)
		}
	}
	if len(ratios) < 4 {
		return 1
	}
	m := median(ratios)
	if math.Abs(m-1) <= 0.05 {
		return 1
	}
	agree := 0
	for _, r := range ratios {
		if math.Abs(r/m-1) <= 0.05 {
			agree++
		}
	}
	if float64(agree) < 0.6*float64(len(ratios)) {
		return 1
	}
	return snapSplitRatio(m)
}

// snapSplitRatio snaps a measured factor to a nearby simple ratio (4, 3/2, 1/10, ...).
func snapSplitRatio(m float64) float64 {
	for d := 1.0; d <= 4; d++ {
		if k := math.Round(m * d); k > 0 && math.Abs(k/d/m-1) < 0.01 {
			return k / d
		}
		if k := math.Round(d / m); k > 0 && math.Abs(d/k/m-1) < 0.01 {
			return d / k
		}
	}
	return m
}

// scaleRowsForSplit divides price and EPS by factor; P/E is split-invariant.
func scaleRowsForSplit(rows []epsRow, factor float64) {
	for i := range rows {
		if rows[i].stockPrice != nil {
			rows[i].stockPrice = ptrFloat(roundTo(*rows[i].stockPrice/factor, 4))
		}
		if rows[i].ttmNetEPS != nil {
			rows[i].ttmNetEPS = ptrFloat(roundTo(*rows[i].ttmNetEPS/factor, 4))
		}
	}
}

func median(v []float64) float64 {
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}

func roundTo(v float64, decimals int) float64 {
	p := math.Pow(10, float64(decimals))
	return math.Round(v*p) / p
}

func hasMoreThan2Decimals(v float64) bool {
	return math.Abs(v*100-math.Round(v*100)) > 1e-6
}

func floatPtrEqual(a, b *float64) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func epsRowEqual(a, b epsRow) bool {
	return a.date == b.date && floatPtrEqual(a.stockPrice, b.stockPrice) &&
		floatPtrEqual(a.ttmNetEPS, b.ttmNetEPS) && floatPtrEqual(a.peRatio, b.peRatio)
}

func fmtEPSRow(r epsRow) string {
	return fmtEPSFloatPtr(r.stockPrice) + "," + fmtEPSFloatPtr(r.ttmNetEPS) + "," + fmtEPSFloatPtr(r.peRatio)
}
