mod cpi;
mod cut;
mod stats;
mod validate;

use std::env;
use std::error::Error;
use std::fs;
use std::path::{Path, PathBuf};
use std::sync::Arc;

use chrono::{Datelike, Local};
use rayon::prelude::*;

use cpi::CpiData;

fn find_stocks_dir() -> Option<PathBuf> {
    let mut dir = env::current_dir().ok()?;
    loop {
        let candidate = dir.join("data").join("stocks");
        if candidate.is_dir() {
            return Some(candidate);
        }
        if !dir.pop() {
            return None;
        }
    }
}

fn process_ticker(ticker_dir: &Path, years: u32, cpi: &CpiData) -> Result<String, String> {
    let ticker = ticker_dir
        .file_name()
        .and_then(|n| n.to_str())
        .unwrap_or("UNKNOWN")
        .to_string();

    let combined = fs::read_to_string(ticker_dir.join("combined.csv"))
        .map_err(|e| format!("{ticker}: {e}"))?;
    validate::validate(&combined).map_err(|e| format!("{ticker}: {e}"))?;
    let cut = cut::cut_to_last_n_years(&combined, years).map_err(|e| format!("{ticker}: {e}"))?;

    // let cut_path = ticker_dir.join("avgpe_cut.csv");
    // fs::write(&cut_path, &cut)
    //     .map_err(|e| format!("{ticker}: {e}"))?;

    let stats =
        stats::compute_stats(&cut, &ticker, years, cpi).map_err(|e| format!("{ticker}: {e}"))?;
    let json = serde_json::to_string_pretty(&stats).map_err(|e| format!("{ticker}: {e}"))?;
    let json_path = ticker_dir.join(format!("avgpe_{years}.json"));
    fs::write(&json_path, json + "\n").map_err(|e| format!("{ticker}: {e}"))?;

    Ok(format!(
        "wrote {json_path_display}",
        json_path_display = json_path.display()
    ))
}

fn main() -> Result<(), Box<dyn Error>> {
    dotenvy::dotenv().ok(); // load .env if present; ignore if missing

    let years: u32 = env::args()
        .nth(1)
        .ok_or("Usage: avgpe <YEARS>")?
        .parse()
        .map_err(|_| "YEARS must be a positive integer")?;

    let current_year = Local::now().year() as u32;
    let cpi = Arc::new(CpiData::fetch(
        current_year.saturating_sub(years + 1),
        current_year,
    )?);

    let stocks_dir = find_stocks_dir().ok_or("could not find data/stocks/")?;

    let mut ticker_dirs: Vec<PathBuf> = fs::read_dir(&stocks_dir)?
        .filter_map(|e| e.ok())
        .map(|e| e.path())
        .filter(|p| p.is_dir())
        .collect();
    ticker_dirs.sort();

    let pool = rayon::ThreadPoolBuilder::new().num_threads(10).build()?;

    let results: Vec<Result<String, String>> = pool.install(|| {
        ticker_dirs
            .par_iter()
            .map(|d| process_ticker(d, years, &cpi))
            .collect()
    });

    let mut errors = 0usize;
    for res in &results {
        match res {
            Ok(msg) => println!("{msg}"),
            Err(msg) => {
                eprintln!("error: {msg}");
                errors += 1;
            }
        }
    }
    if errors > 0 {
        return Err(format!("{errors} ticker(s) failed").into());
    }
    Ok(())
}
