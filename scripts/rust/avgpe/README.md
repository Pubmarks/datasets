# avgpe

Reads every `data/stocks/<TICKER>/combined.csv`, cuts to the last N years,
computes P/E statistics including Shiller P/E (CAPE), and writes `avgpe_<N>.json`
per ticker. All tickers are processed in parallel (10 threads). Requires a
`FRED_API_KEY` in `.env` to fetch CPI data.

## Usage

```
cargo run -- <YEARS>
```

`cargo run -- 10` → writes `avgpe_10.json` in each ticker directory.

## Pipeline

```
// startup (once)
load .env
fetch monthly CPI from FRED (CPIAUCSL) for years [today-(N+1) .. today]

// per ticker (parallel, 10 threads)
for each data/stocks/<TICKER>/combined.csv:

    // validate
    assert columns: date, pe_ratio, ttm_net_eps, close

    // cut
    cutoff = today - N years (same month/day)
    keep rows where date >= cutoff

    // per-row P/E
    for each row:
        pe = close / ttm_net_eps          // skip if eps == 0 or missing

        if pe > 0:
            update positive band min/max
            accumulate into pos_values[]
        else:
            update lossy band min/max      // max_lossy = closest to 0, min_lossy = most negative
            accumulate into lossy_values[]

        // Shiller: track last EPS seen per calendar month (both signs)
        month_end_eps[(year, month)] = eps // overwrites — last row of month wins

    // distribution (positive band)
    p_e_mean   = mean(pos_values)
    p_e_median = median(pos_values)
    p_e_mode   = most-frequent rounded bucket; lowest bucket wins ties

    // harmonic mean P/E (all non-zero EPS rows, both signs)
    p_e_harmonic = 1 / mean(eps / close for each row where eps != 0)

    // distribution (lossy band) — null if no loss rows
    p_e_mean_lossy / p_e_median_lossy / p_e_mode_lossy

    // Shiller P/E (CAPE) — trailing N * 12 monthly observations
    for each (year, month) in latest (N * 12) entries of month_end_eps:
        cpi_ym   = CPI[(year, month)] or most recent prior published CPI
        real_eps = eps * (latest_cpi / cpi_ym)

    avg_real_eps = mean(real_eps values across the N * 12 months)
    p_e_shiller  = last_price / avg_real_eps             // null if avg_real_eps == 0

    write avgpe_<N>.json
```

## Output shape

See `shape.json` for a live example. Field reference:

| Field | Type | Description |
|---|---|---|
| `ticker` | string | Ticker symbol |
| `start_date` | string | First date in the cut window |
| `end_date` | string | Last date in the cut window |
| `eps_last` | float | Last TTM net EPS |
| `p_e_min` | float (4dp) | Lowest positive P/E in the window |
| `p_e_min_date` | string | Date of `p_e_min` |
| `p_e_max` | float (4dp) | Highest positive P/E in the window |
| `p_e_max_date` | string | Date of `p_e_max` |
| `p_e_mean` | float (4dp) | Mean of positive P/E |
| `p_e_median` | float (4dp) | Median of positive P/E |
| `p_e_mode` | integer | Most frequent P/E bucket (rounded; lowest wins ties) |
| `p_e_max_lossy` | float (4dp) \| null | Highest negative P/E (closest to zero); null if no loss periods |
| `p_e_max_lossy_date` | string \| null | Date of `p_e_max_lossy` |
| `p_e_min_lossy` | float (4dp) \| null | Lowest negative P/E (most negative); null if no loss periods |
| `p_e_min_lossy_date` | string \| null | Date of `p_e_min_lossy` |
| `p_e_mean_lossy` | float (4dp) \| null | Mean of lossy P/E; null if no loss periods |
| `p_e_median_lossy` | float (4dp) \| null | Median of lossy P/E; null if no loss periods |
| `p_e_mode_lossy` | integer \| null | Mode bucket of lossy P/E; null if no loss periods |
| `p_e_last` | float (4dp) | P/E of the last row (`close / ttm_net_eps`) |
| `p_e_shiller` | float (4dp) \| null | Shiller P/E (CAPE): `last_price / avg_real_eps` across N years |
| `p_e_harmonic` | float (4dp) \| null | Harmonic mean P/E: `1 / mean(EPS/price)` across all daily rows; less sensitive to near-zero EPS blowup than `p_e_mean` |
| `price_last` | float | Last close price |

### Shiller P/E notes

- Uses one EPS observation per calendar month (last row of each month) across the latest `N × 12` months (`60` for 5yr, `120` for 10yr), matching Shiller's monthly CAPE methodology
- Capping to `N × 12` months drops any extra partial boundary month when a date cut spans `N × 12 + 1` calendar months mid-month
- All months included — loss periods (negative EPS) lower the average, raising CAPE
- EPS adjusted to latest published CPI month's dollars via monthly FRED `CPIAUCSL` observations (`latest_cpi / cpi_ym`)
- If the current calendar month's CPI is not yet published, falls back to the most recent prior month's CPI (`latest_cpi / latest_cpi = 1.0`, leaving current EPS unchanged)
- CPI series: `CPIAUCSL` (Consumer Price Index for All Urban Consumers, monthly, seasonally adjusted)
