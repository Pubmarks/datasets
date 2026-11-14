#!/usr/bin/env python3
# /// script
# requires-python = ">=3.11"
# dependencies = []
# ///
"""Update README.md stocks table and tickers.json from data/stocks/."""

from __future__ import annotations

import json
import sys
from pathlib import Path


def _year_range(ticker_dir: Path, filename: str) -> str | None:
    years = sorted(
        d.name
        for d in ticker_dir.iterdir()
        if d.is_dir() and len(d.name) == 4 and d.name.isdigit()
        and (d / filename).exists()
    )
    if not years:
        return None
    return f"{years[0]}–{years[-1]}" if len(years) > 1 else years[0]


CDN_BASE = "https://pubmarks.github.io/datasets/stocks"


def iter_stock_dirs(data_root: Path) -> list[Path]:
    if not data_root.is_dir():
        return []
    dirs: list[Path] = []
    for ticker_dir in sorted(data_root.iterdir()):
        if not ticker_dir.is_dir():
            continue
        if not (ticker_dir / "ohlcv.csv").exists():
            continue
        dirs.append(ticker_dir)
    return dirs


def build_rows(data_root: Path) -> list[str]:
    rows: list[str] = []
    for ticker_dir in iter_stock_dirs(data_root):
        ticker = ticker_dir.name.upper()
        ohlcv_url = f"{CDN_BASE}/{ticker_dir.name}/ohlcv.csv"
        eps_url = f"{CDN_BASE}/{ticker_dir.name}/eps.csv"
        combined_url = f"{CDN_BASE}/{ticker_dir.name}/combined.csv"
        avgpe5_url = f"{CDN_BASE}/{ticker_dir.name}/avgpe_5.json"
        avgpe10_url = f"{CDN_BASE}/{ticker_dir.name}/avgpe_10.json"

        ohlcv_label = _year_range(ticker_dir, "ohlcv.csv") or "—"
        eps_label = _year_range(ticker_dir, "eps.csv")
        combined_label = _year_range(ticker_dir, "ohlcv.csv") or "—"

        ohlcv_cell = f"[{ohlcv_label}]({ohlcv_url})"
        eps_cell = f"[{eps_label}]({eps_url})" if eps_label else "—"

        has_combined = (ticker_dir / "combined.csv").exists()
        combined_cell = f"[{combined_label}]({combined_url})" if has_combined else "—"

        has_avgpe5 = (ticker_dir / "avgpe_5.json").exists()
        has_avgpe10 = (ticker_dir / "avgpe_10.json").exists()
        if has_avgpe5 or has_avgpe10:
            parts = []
            if has_avgpe5:
                parts.append(f"[5yr]({avgpe5_url})")
            if has_avgpe10:
                parts.append(f"[10yr]({avgpe10_url})")
            avgpe_cell = " ".join(parts)
        else:
            avgpe_cell = "—"

        rows.append(f"| {ticker} | {ohlcv_cell} | {eps_cell} | {combined_cell} | {avgpe_cell} |")
    return rows


def update_readme(readme: Path, rows: list[str]) -> None:
    lines = readme.read_text(encoding="utf-8").splitlines(keepends=True)

    # Locate ### Stocks heading
    start = next(
        (i for i, l in enumerate(lines) if l.rstrip("\n") == "### Stocks"), None
    )
    if start is None:
        print("README.md: '### Stocks' heading not found", file=sys.stderr)
        sys.exit(1)

    # Find first table row after heading (skip blanks and non-table lines)
    table_found = False
    j = start + 1
    while j < len(lines):
        if lines[j].strip() == "":
            j += 1
            continue
        if lines[j].lstrip().startswith("|"):
            table_found = True
            break
        j += 1

    # Find end of existing table (k == j when no table was present)
    k = j
    while k < len(lines) and lines[k].lstrip().startswith("|"):
        k += 1

    header = ["| Ticker | OHLCV | EPS | Combined | Avg P/E |\n", "| ------ | ----- | --- | -------- | ------- |\n"]
    body = [r + "\n" for r in rows]

    if not table_found and j > 0 and lines[j - 1].strip() != "":
        header = ["\n"] + header

    readme.write_text("".join(lines[:j] + header + body + lines[k:]), encoding="utf-8")
    print(f"Updated README.md: {len(rows)} ticker(s)")


def write_tickers_json(path: Path, data_root: Path) -> None:
    tickers = [d.name.upper() for d in iter_stock_dirs(data_root)]
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(
        json.dumps({"tickers": tickers}, indent=2) + "\n",
        encoding="utf-8",
    )
    print(f"Updated {path}: {len(tickers)} ticker(s)")


def main() -> None:
    data_root = Path("data/stocks")
    rows = build_rows(data_root)
    update_readme(Path("README.md"), rows)
    write_tickers_json(data_root / "tickers.json", data_root)


if __name__ == "__main__":
    main()
