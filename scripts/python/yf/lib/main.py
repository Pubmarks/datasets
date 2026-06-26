"""CLI entrypoint for `yf ohlcv`."""

from __future__ import annotations

import datetime as dt
import subprocess
from pathlib import Path

import click

from lib.csvfmt import (
    append_ohlcv_rows,
    err,
    read_last_date,
    read_ohlcv_rows,
    write_ohlcv_csv_atomic,
)
from lib.ohlcv_fetch import fetch_daily_ohlcv
from lib.ticker_year import parse_ticker_year


def _repo_root() -> Path:
    try:
        out = subprocess.check_output(
            ["git", "rev-parse", "--show-toplevel"],
            stderr=subprocess.DEVNULL,
        )
        return Path(out.decode().strip())
    except subprocess.CalledProcessError:
        pass
    here = Path(__file__).resolve().parent
    for parent in [here, *here.parents]:
        if (parent / "data").is_dir():
            return parent
    raise RuntimeError("cannot locate repo root (no git root and no data/ directory found)")


def _weekend_cutoff(today: dt.date, base: dt.date) -> dt.date:
    if today.weekday() == 5:
        return min(base, today - dt.timedelta(days=1))
    if today.weekday() == 6:
        return min(base, today - dt.timedelta(days=2))
    return base


def _sync_year_files(root: Path, ticker: str, all_rows: list, force_year: int | None = None) -> None:
    """Create or incrementally update per-year files from flat file data."""
    by_year: dict[int, list] = {}
    for row in all_rows:
        y = int(row[0][:4])
        by_year.setdefault(y, []).append(row)

    for year, year_rows in sorted(by_year.items()):
        year_path = root / "data" / "stocks" / ticker / str(year) / "ohlcv.csv"

        if year == force_year:
            write_ohlcv_csv_atomic(year_rows, year_path)
            err(f"wrote {len(year_rows)} row(s) to {year_path}")
            continue

        year_last_d = read_last_date(year_path)
        flat_year_last_d = dt.date.fromisoformat(year_rows[-1][0])

        if year_last_d == flat_year_last_d:
            continue

        if year_last_d is None:
            write_ohlcv_csv_atomic(year_rows, year_path)
            err(f"wrote {len(year_rows)} row(s) to {year_path}")
        else:
            missing = [r for r in year_rows if dt.date.fromisoformat(r[0]) > year_last_d]
            if missing:
                append_ohlcv_rows(missing, year_path)
                err(f"appended {len(missing)} row(s) to {year_path}")


def _need_subcommand() -> None:
    err(
        "specify a subcommand: ohlcv\n\n"
        "Examples:\n"
        "  yf ohlcv AAPL\n"
        "  yf ohlcv MSFT 2024\n\n"
        'Run "yf --help" for full usage.'
    )
    raise SystemExit(1)


@click.group(invoke_without_command=True)
@click.pass_context
def yf(ctx: click.Context) -> None:
    if ctx.invoked_subcommand is None:
        _need_subcommand()


@yf.command(
    "ohlcv",
    context_settings={"show_default": True},
    help=(
        "Download daily OHLCV and write to data/stocks/TICKER[/YEAR]/ohlcv.csv. "
        "Without a year: syncs flat file + all per-year files (one Yahoo call). "
        "With a year: updates only that year file."
    ),
)
@click.argument("parts", nargs=-1)
def ohlcv_cmd(parts: tuple[str, ...]) -> None:
    try:
        ticker, year, year_set = parse_ticker_year(parts)
    except ValueError as e:
        err(str(e))
        raise SystemExit(1) from e

    try:
        root = _repo_root()
    except RuntimeError as e:
        err(str(e))
        raise SystemExit(1) from e

    today = dt.date.today()

    if year_set:
        # --- explicit year: update only that year file ---
        path = root / "data" / "stocks" / ticker / str(year) / "ohlcv.csv"
        last_d = read_last_date(path)
        from_d = last_d + dt.timedelta(days=1) if last_d else None
        cutoff = _weekend_cutoff(today, dt.date(year, 12, 31))

        if from_d and from_d > cutoff:
            err(f"{path}: already up to date")
            return

        try:
            rows = fetch_daily_ohlcv(ticker, year=year, year_set=True, from_date=from_d)
        except Exception as e:
            err(str(e))
            raise SystemExit(1) from e

        if last_d is None:
            write_ohlcv_csv_atomic(rows, path)
        else:
            append_ohlcv_rows(rows, path)
        err(f"wrote {len(rows)} row(s) to {path}")

    else:
        # --- no year: refresh the flat file, then sync all per-year files ---
        flat_path = root / "data" / "stocks" / ticker / "ohlcv.csv"
        existing = read_ohlcv_rows(flat_path)

        if not existing:
            # New ticker (or empty/corrupt flat file): backfill full history in one
            # "period=max" Yahoo call.
            try:
                merged = fetch_daily_ohlcv(ticker, year=None, year_set=False, from_date=None)
            except Exception as e:
                err(str(e))
                raise SystemExit(1) from e
            flat_path.parent.mkdir(parents=True, exist_ok=True)
            write_ohlcv_csv_atomic(merged, flat_path)
            err(f"wrote {len(merged)} row(s) of history to {flat_path}")
        else:
            # Existing ticker: re-fetch only the current year, keep prior years as-is.
            try:
                new_year_rows = fetch_daily_ohlcv(ticker, year=today.year, year_set=True, from_date=None)
            except Exception as e:
                err(str(e))
                raise SystemExit(1) from e
            prior_rows = [r for r in existing if int(r[0][:4]) < today.year]
            merged = prior_rows + new_year_rows
            flat_path.parent.mkdir(parents=True, exist_ok=True)
            write_ohlcv_csv_atomic(merged, flat_path)
            err(f"wrote {len(new_year_rows)} current-year row(s) to {flat_path}")

        _sync_year_files(root, ticker, merged, force_year=today.year)


def main() -> None:
    yf()


if __name__ == "__main__":
    main()
