use std::collections::BTreeMap;
use std::error::Error;

use serde::Deserialize;

const FRED_BASE: &str = "https://api.stlouisfed.org/fred/series/observations";
const SERIES_CPI: &str = "CPIAUCSL";

#[derive(Deserialize)]
struct FredResponse {
    observations: Option<Vec<Observation>>,
    error_message: Option<String>,
}

#[derive(Deserialize)]
struct Observation {
    date: String,
    value: String,
}

/// Parse `"YYYY-MM"` or `"YYYY-MM-DD"` into `(year, month)`.
pub fn parse_year_month(date: &str) -> Option<(u32, u32)> {
    if date.len() < 7 || date.as_bytes()[4] != b'-' {
        return None;
    }
    let year = date[..4].parse::<u32>().ok()?;
    let month = date[5..7].parse::<u32>().ok()?;
    if !(1..=12).contains(&month) {
        return None;
    }
    Some((year, month))
}

/// Monthly CPI values keyed by `(year, month)`, sourced from FRED (`CPIAUCSL`).
///
/// Adjusts historical nominal EPS values to the latest published CPI month's
/// dollars (`latest_cpi / cpi_at_month`).
pub struct CpiData {
    by_month: BTreeMap<(u32, u32), f64>,
    latest_cpi: f64,
}

impl CpiData {
    /// Fetch monthly CPI from FRED for the given year range (inclusive).
    ///
    /// Reads `FRED_API_KEY` from the environment — call `dotenvy::dotenv()`
    /// before this if your key lives in a `.env` file.
    pub fn fetch(start_year: u32, end_year: u32) -> Result<Self, Box<dyn Error>> {
        let api_key = std::env::var("FRED_API_KEY").map_err(|_| "FRED_API_KEY not set")?;

        let url = format!(
            "{FRED_BASE}?series_id={SERIES_CPI}\
             &observation_start={start_year}-01-01\
             &observation_end={end_year}-12-31\
             &api_key={api_key}\
             &file_type=json"
        );

        let resp: FredResponse = reqwest::blocking::get(&url)?.json()?;

        if let Some(msg) = resp.error_message {
            return Err(format!("FRED error: {msg}").into());
        }

        let mut by_month: BTreeMap<(u32, u32), f64> = BTreeMap::new();
        for obs in resp.observations.as_deref().unwrap_or(&[]) {
            // FRED uses "." for missing values
            if obs.value == "." {
                continue;
            }
            if let (Some(ym), Ok(cpi)) = (parse_year_month(&obs.date), obs.value.parse::<f64>()) {
                by_month.insert(ym, cpi);
            }
        }

        if by_month.is_empty() {
            return Err("FRED returned no usable CPI observations".into());
        }

        let latest_cpi = *by_month.values().next_back().unwrap();

        Ok(Self {
            by_month,
            latest_cpi,
        })
    }

    /// Adjust a nominal EPS from `(year, month)` to the latest CPI month's dollars.
    ///
    /// Uses that month's CPI when available, or the most recent prior published
    /// month when `ym` isn't published yet (e.g. the current calendar month,
    /// where `latest_cpi / latest_cpi == 1.0` leaves EPS unchanged).
    pub fn adjust_eps_or_nominal(&self, eps: f64, ym: (u32, u32)) -> f64 {
        match self.by_month.range(..=ym).next_back() {
            Some((_, &base)) if base > 0.0 => eps * (self.latest_cpi / base),
            _ => eps,
        }
    }

    /// Empty instance with no CPI data — `adjust_eps_or_nominal` returns EPS unchanged.
    #[cfg(test)]
    pub fn empty() -> Self {
        Self {
            by_month: BTreeMap::new(),
            latest_cpi: 0.0,
        }
    }

    #[cfg(test)]
    pub fn from_map(by_month: BTreeMap<(u32, u32), f64>) -> Self {
        let latest_cpi = by_month.values().next_back().copied().unwrap_or(0.0);
        Self {
            by_month,
            latest_cpi,
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn parses_year_month_from_iso_dates() {
        assert_eq!(parse_year_month("2026-11-13"), Some((2026, 11)));
        assert_eq!(parse_year_month("2016-01"), Some((2016, 1)));
        assert_eq!(parse_year_month("2026-13-01"), None);
        assert_eq!(parse_year_month("2026"), None);
    }

    #[test]
    fn adjusts_historical_month_and_falls_back_to_prior_for_unreleased_current_month() {
        let cpi = CpiData::from_map(
            [((2020, 6), 250.0), ((2026, 10), 300.0)]
                .into_iter()
                .collect(),
        );
        // 2020-06 scaled by 300 / 250 = 1.2
        assert!((cpi.adjust_eps_or_nominal(2.0, (2020, 6)) - 2.4).abs() < 1e-12);
        // 2026-11 (not yet released) uses 2026-10 (300 / 300 = 1.0)
        assert!((cpi.adjust_eps_or_nominal(5.0, (2026, 11)) - 5.0).abs() < 1e-12);
    }
}
