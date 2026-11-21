package main

import (
	"context"
	"errors"
	"fmt"
	"io"
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
		Long: `Download price-to-earnings and EPS history from MacroTrends (PE-ratio HTML table).

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
			rows, err := fetchEPSHistory(ctx, normalizeSymbol(ticker))
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
			out, splitFactor, err := mergeEPSWithExisting(out, outPath)
			if err != nil {
				return err
			}
			if splitFactor != 1 {
				fmt.Fprintf(cmd.ErrOrStderr(), "split basis change detected (factor %g): rescaled rows older than MacroTrends' history\n", splitFactor)
			}

			if err := writeEPSCSVAtomic(out, outPath); err != nil {
				return err
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "wrote %d row(s) to %s\n", len(out), outPath)
			return syncEPSYearFiles(root, normalizeSymbol(ticker), out, cmd.ErrOrStderr())
		},
	}
	cmd.Flags().BoolVar(&write, "write", false, "write to data/stocks/TICKER/eps.csv + per-year files (update or create)")
	cmd.AddCommand(newEpsRebaseCmd())
	return cmd
}

func newEpsRebaseCmd() *cobra.Command {
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "rebase [ticker...]",
		Short: "Put eps.csv prices and P/E on the same basis as ohlcv.csv (offline)",
		Long: `Correct data/stocks/TICKER/eps.csv using data/stocks/TICKER/ohlcv.csv. No network access.

For every row except the newest: stock_price becomes the close of the last trading day
on or before the date, pe_ratio is rescaled by close / stock_price, and EPS stored at
2 decimals in rows older than MacroTrends' history is recovered as stock_price / pe_ratio.
Rows are never added or removed, and a second run changes nothing.

Without tickers, every directory under data/stocks that has an eps.csv is processed.
A ticker whose recent prices disagree with ohlcv.csv is skipped with a warning.`,
		Example: `  macrotrends eps rebase
  macrotrends eps rebase --dry-run AAPL MSFT`,
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := repoRoot()
			if err != nil {
				return err
			}
			tickers := make([]string, 0, len(args))
			for _, a := range args {
				tickers = append(tickers, normalizeSymbol(a))
			}
			if len(tickers) == 0 {
				paths, err := filepath.Glob(filepath.Join(root, "data", "stocks", "*", "eps.csv"))
				if err != nil {
					return err
				}
				for _, p := range paths {
					tickers = append(tickers, filepath.Base(filepath.Dir(p)))
				}
				sort.Strings(tickers)
			}

			w := cmd.ErrOrStderr()
			failed := 0
			for _, ticker := range tickers {
				changes, err := rebaseTicker(root, ticker, dryRun)
				switch {
				case errors.Is(err, errBasisMismatch):
					fmt.Fprintf(w, "%s: warning: skipped: %v\n", ticker, err)
				case err != nil:
					failed++
					fmt.Fprintf(w, "%s: error: %v\n", ticker, err)
				case len(changes) == 0:
					fmt.Fprintf(w, "%s: up to date\n", ticker)
				default:
					verb := "rebased"
					if dryRun {
						verb = "would rebase"
					}
					fmt.Fprintf(w, "%s: %s %d row(s), %s..%s\n", ticker, verb, len(changes), changes[0].date, changes[len(changes)-1].date)
					if dryRun {
						for _, c := range changes {
							fmt.Fprintf(cmd.OutOrStdout(), "%s,%s,%s -> %s\n", ticker, c.date, fmtEPSRow(c.old), fmtEPSRow(c.new))
						}
					}
				}
			}
			if failed > 0 {
				return fmt.Errorf("%d ticker(s) failed", failed)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print the rows that would change (ticker,date,old -> new) without writing")
	return cmd
}

// rebaseTicker rebases one ticker's eps.csv (and per-year files) against its ohlcv.csv.
func rebaseTicker(root, ticker string, dryRun bool) ([]rebaseChange, error) {
	dir := filepath.Join(root, "data", "stocks", ticker)
	epsPath := filepath.Join(dir, "eps.csv")
	rows, err := readEPSCSVRows(epsPath)
	if err != nil {
		return nil, err
	}
	if rows == nil {
		return nil, fmt.Errorf("%s not found", epsPath)
	}
	px, err := readCloseSeries(filepath.Join(dir, "ohlcv.csv"))
	if err != nil {
		return nil, err
	}
	out, changes, err := rebaseEPSRows(rows, px)
	if err != nil {
		return nil, err
	}
	// history must never be lost: same rows, same dates, same order
	if len(out) != len(rows) {
		return nil, fmt.Errorf("rebase changed row count (%d -> %d); nothing written", len(rows), len(out))
	}
	for i := range rows {
		if out[i].date != rows[i].date {
			return nil, fmt.Errorf("rebase changed date on row %d; nothing written", i+1)
		}
	}
	if dryRun || len(changes) == 0 {
		return changes, nil
	}
	if err := writeEPSCSVAtomic(out, epsPath); err != nil {
		return nil, err
	}
	return changes, syncEPSYearFiles(root, ticker, out, io.Discard)
}

func repoRoot() (string, error) {
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", fmt.Errorf("cannot find repo root: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}
