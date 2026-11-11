package main

import (
	"context"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

func newOhlcvCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ohlcv [ticker] [year]",
		Short: "Download daily OHLCV as CSV to stdout",
		Long: `Download daily OHLCV from MacroTrends (chart iframe + stock_data_download CSV).

Ticker: TICKER environment variable and/or first argument.
Year:   YEAR environment and/or last argument; a single 4-digit argument sets year only (ticker from env).
If year is omitted, all rows from the download are printed.`,
		Example: `  macrotrends ohlcv AAPL
  macrotrends ohlcv MSFT 2024
  TICKER=AAPL macrotrends ohlcv 2024`,
		Args:          cobra.RangeArgs(0, 2),
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			ticker, year, yearSet, err := parseTickerYear(args)
			if err != nil {
				return err
			}

			ctx := context.Background()
			var yb *int
			if yearSet {
				v := yearsBackForYear(year)
				yb = &v
			}

			csvBytes, err := fetchOHLCVCSV(ctx, normalizeSymbol(ticker), yb)
			if err != nil {
				return err
			}
			rows, err := parseMacroTrendsCSV(strings.NewReader(string(csvBytes)))
			if err != nil {
				return err
			}
			out := rows
			if yearSet {
				from := time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC)
				to := time.Date(year, 12, 31, 23, 59, 59, 0, time.UTC)
				out = filterByRange(rows, from, to)
			}
			return writeCSV(cmd.OutOrStdout(), out)
		},
	}
}
