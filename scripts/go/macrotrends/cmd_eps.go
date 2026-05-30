package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

func newEpsCmd() *cobra.Command {
	var write bool
	cmd := &cobra.Command{
		Use:   "eps [ticker] [year]",
		Short: "Download historical EPS/P/E table as CSV to stdout",
		Long: `Download price-to-earnings and EPS history from MacroTrends (chart redirect + /pe-ratio HTML table).

Ticker: TICKER environment variable and/or first argument.
Year:   YEAR environment and/or last argument; a single 4-digit argument sets year only (ticker from env).
If year is omitted, all parsed rows are printed.

Columns: date, stock_price, ttm_net_eps, pe_ratio (empty cells when MacroTrends omits a value).

With --write: writes/updates data/stocks/TICKER/eps.csv and per-year files in the repo root.`,
		Example: `  macrotrends eps AAPL
  macrotrends eps MSFT 2024
  TICKER=AAPL macrotrends eps 2024
  macrotrends eps --write AAPL`,
		Args:          cobra.RangeArgs(0, 2),
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			ticker, year, yearSet, err := parseTickerYear(args)
			if err != nil {
				return err
			}

			ctx := context.Background()
			client := newHTTPClient(httpTimeout)

			rows, err := fetchEPSHistory(ctx, client, normalizeSymbol(ticker))
			if err != nil {
				return err
			}
			out := rows
			if yearSet {
				from := time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC)
				to := time.Date(year, 12, 31, 23, 59, 59, 0, time.UTC)
				out = filterEPSRowsByRange(rows, from, to)
			}

			if !write {
				return writeEPSCSV(cmd.OutOrStdout(), out)
			}

			root, err := repoRoot()
			if err != nil {
				return err
			}
			outPath := filepath.Join(root, "data", "stocks", normalizeSymbol(ticker), "eps.csv")
			if err := os.MkdirAll(filepath.Dir(outPath), 0755); err != nil {
				return err
			}

			// store oldest-first in files; stdout keeps website order (newest-first)
			sort.Slice(out, func(i, j int) bool { return out[i].date < out[j].date })

			// preserve any rows the file has that predate MacroTrends' current history
			out, err = mergeEPSWithExisting(out, outPath)
			if err != nil {
				return err
			}

			if err := writeEPSCSVAtomic(out, outPath); err != nil {
				return err
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "wrote %d row(s) to %s\n", len(out), outPath)
			return syncEPSYearFiles(root, normalizeSymbol(ticker), out, cmd.ErrOrStderr())
		},
	}
	cmd.Flags().BoolVar(&write, "write", false, "write to data/stocks/TICKER/eps.csv + per-year files (update or create)")
	return cmd
}

func repoRoot() (string, error) {
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", fmt.Errorf("cannot find repo root: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}
