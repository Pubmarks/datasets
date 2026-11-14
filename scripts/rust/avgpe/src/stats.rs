use std::collections::{BTreeMap, HashMap};
use std::error::Error;
use std::io::Cursor;

use serde::Serialize;

use crate::cpi::{CpiData, parse_year_month};

#[derive(Serialize)]
pub struct Stats {
    pub ticker: String,
    pub start_date: String,
    pub end_date: String,
    pub eps_last: f64,
    pub p_e_last: f64,
    pub price_last: f64,
    pub p_e_harmonic: Option<f64>,
    pub p_e_max_date: String,
    pub p_e_max_lossy_date: Option<String>,
    pub p_e_max_lossy: Option<f64>,
    pub p_e_max: f64,
    pub p_e_mean_lossy: Option<f64>,
    pub p_e_mean: f64,
    pub p_e_median_lossy: Option<f64>,
    pub p_e_median: f64,
    pub p_e_min_date: String,
    pub p_e_min_lossy_date: Option<String>,
    pub p_e_min_lossy: Option<f64>,
    pub p_e_min: f64,
    pub p_e_mode_lossy: Option<i64>,
    pub p_e_mode: i64,
    pub p_e_shiller: Option<f64>,
}

pub fn compute_stats(
    cut: &str,
    ticker: &str,
    years: u32,
    cpi: &CpiData,
) -> Result<Stats, Box<dyn Error>> {
    let mut reader = csv::Reader::from_reader(Cursor::new(cut));
    let headers = reader.headers()?.clone();

    let i_date = headers
        .iter()
        .position(|c| c == "date")
        .ok_or("missing date")?;
    let i_close = headers
        .iter()
        .position(|c| c == "close")
        .ok_or("missing close")?;
    let i_eps = headers
        .iter()
        .position(|c| c == "ttm_net_eps")
        .ok_or("missing ttm_net_eps")?;

    let mut start_date = String::new();
    let mut end_date = String::new();
    // positive band
    let mut min_pe = f64::MAX;
    let mut min_date = String::new();
    let mut max_pe = f64::MIN;
    let mut max_date = String::new();
    // lossy band: max_lossy = closest to zero (least negative), min_lossy = most negative
    let mut max_lossy_pe: Option<f64> = None;
    let mut max_lossy_date: Option<String> = None;
    let mut min_lossy_pe: Option<f64> = None;
    let mut min_lossy_date: Option<String> = None;
    let mut pos_values: Vec<f64> = Vec::new();
    let mut lossy_values: Vec<f64> = Vec::new();
    let mut ep_values: Vec<f64> = Vec::new();
    // (year, month) → last EPS seen in that calendar month (month-end value wins via overwrite)
    let mut month_end_eps: BTreeMap<(u32, u32), f64> = BTreeMap::new();
    let mut last_pe = 0f64;
    let mut last_price = 0f64;
    let mut last_eps = 0f64;

    for result in reader.records() {
        let record = result?;
        let date = record.get(i_date).unwrap_or("").to_string();
        let close = record.get(i_close).and_then(|v| v.parse::<f64>().ok());
        let eps = record.get(i_eps).and_then(|v| v.parse::<f64>().ok());

        if start_date.is_empty() {
            start_date = date.clone();
        }
        end_date = date.clone();

        if let (Some(c), Some(e)) = (close, eps)
            && e != 0.0
        {
            let pe = c / e;
            if pe > 0.0 {
                if pe < min_pe {
                    min_pe = pe;
                    min_date = date.clone();
                }
                if pe > max_pe {
                    max_pe = pe;
                    max_date = date.clone();
                }
                pos_values.push(pe);
            } else {
                // lossy: max_lossy = closest to zero, min_lossy = most negative
                if max_lossy_pe.is_none_or(|h| pe > h) {
                    max_lossy_pe = Some(pe);
                    max_lossy_date = Some(date.clone());
                }
                if min_lossy_pe.is_none_or(|l| pe < l) {
                    min_lossy_pe = Some(pe);
                    min_lossy_date = Some(date.clone());
                }
                lossy_values.push(pe);
            }
            // track month-end EPS for Shiller CAPE (both signs, last row of month wins)
            if let Some(ym) = parse_year_month(&date) {
                month_end_eps.insert(ym, e);
            }
            ep_values.push(e / c);
            last_pe = pe;
            last_price = c;
            last_eps = e;
        }
    }

    if start_date.is_empty() {
        return Err("no rows in cut data".into());
    }
    if min_pe == f64::MAX {
        return Err("no rows with valid pe found".into());
    }

    let round4 = |v: f64| (v * 10000.0).round() / 10000.0;

    let max_cape_months = (years as usize).saturating_mul(12).max(1);
    let p_e_shiller = if month_end_eps.is_empty() {
        None
    } else {
        let skip = month_end_eps.len().saturating_sub(max_cape_months);
        let count = month_end_eps.len() - skip;
        let avg_real_eps = month_end_eps
            .iter()
            .skip(skip)
            .map(|(&ym, &e)| cpi.adjust_eps_or_nominal(e, ym))
            .sum::<f64>()
            / count as f64;
        if avg_real_eps != 0.0 {
            Some(round4(last_price / avg_real_eps))
        } else {
            None
        }
    };

    let p_e_harmonic = if ep_values.is_empty() {
        None
    } else {
        let mean_ep = ep_values.iter().sum::<f64>() / ep_values.len() as f64;
        if mean_ep != 0.0 {
            Some(round4(1.0 / mean_ep))
        } else {
            None
        }
    };

    let (pos_mean, pos_median, pos_mode) =
        distribution(&mut pos_values).expect("already validated non-empty");
    let lossy_dist = distribution(&mut lossy_values);

    Ok(Stats {
        ticker: ticker.to_string(),
        start_date,
        end_date,
        eps_last: last_eps,
        p_e_max_lossy: max_lossy_pe.map(round4),
        p_e_max_lossy_date: max_lossy_date,
        p_e_min_lossy: min_lossy_pe.map(round4),
        p_e_min_lossy_date: min_lossy_date,
        p_e_min: round4(min_pe),
        p_e_min_date: min_date,
        p_e_max: round4(max_pe),
        p_e_max_date: max_date,
        p_e_mean: round4(pos_mean),
        p_e_median: round4(pos_median),
        p_e_mode: pos_mode,
        p_e_mean_lossy: lossy_dist.map(|(mean, _, _)| round4(mean)),
        p_e_median_lossy: lossy_dist.map(|(_, med, _)| round4(med)),
        p_e_mode_lossy: lossy_dist.map(|(_, _, mode)| mode),
        p_e_last: round4(last_pe),
        p_e_shiller,
        p_e_harmonic,
        price_last: last_price,
    })
}

fn distribution(values: &mut [f64]) -> Option<(f64, f64, i64)> {
    if values.is_empty() {
        return None;
    }
    let mean = values.iter().sum::<f64>() / values.len() as f64;
    values.sort_by(|a, b| a.partial_cmp(b).unwrap());
    let n = values.len();
    let median = if n % 2 == 1 {
        values[n / 2]
    } else {
        (values[n / 2 - 1] + values[n / 2]) / 2.0
    };
    let mut counts: HashMap<i64, usize> = HashMap::new();
    for &v in values.iter() {
        *counts.entry(v.round() as i64).or_insert(0) += 1;
    }
    let max_count = *counts.values().max().unwrap();
    let mode = *counts
        .iter()
        .filter(|&(_, &c)| c == max_count)
        .map(|(k, _)| k)
        .min()
        .unwrap();
    Some((mean, median, mode))
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::cpi::CpiData;

    // Empty CPI stub — adjust_eps_or_nominal returns EPS unchanged (factor 1.0).
    fn no_cpi() -> CpiData {
        CpiData::empty()
    }

    const CUT: &str = "\
date,open,high,low,close,volume,ttm_net_eps,pe_ratio
2021-01-04,100,110,90,200.00,1000,2.00,100.00
2022-06-15,105,115,95,105.00,2000,5.00,21.00
2023-03-20,90,100,80,180.00,3000,3.00,60.00
2024-12-31,120,130,110,250.00,4000,4.00,62.50";

    #[test]
    fn min_and_max_pe_are_correct() {
        let s = compute_stats(CUT, "TEST", 5, &no_cpi()).unwrap();
        // pe values: 100.0, 21.0, 60.0, 62.5
        assert_eq!(s.p_e_min, 21.0);
        assert_eq!(s.p_e_min_date, "2022-06-15");
        assert_eq!(s.p_e_max, 100.0);
        assert_eq!(s.p_e_max_date, "2021-01-04");
    }

    #[test]
    fn last_row_fields_are_correct() {
        let s = compute_stats(CUT, "TEST", 5, &no_cpi()).unwrap();
        assert_eq!(s.p_e_last, 62.5);
        assert_eq!(s.price_last, 250.0);
        assert_eq!(s.eps_last, 4.0);
    }

    #[test]
    fn start_and_end_dates_are_correct() {
        let s = compute_stats(CUT, "TEST", 5, &no_cpi()).unwrap();
        assert_eq!(s.start_date, "2021-01-04");
        assert_eq!(s.end_date, "2024-12-31");
    }

    #[test]
    fn rows_with_missing_eps_are_excluded_from_pe() {
        let cut = "\
date,open,high,low,close,volume,ttm_net_eps,pe_ratio
2021-01-04,100,110,90,200.00,1000,,
2022-06-15,105,115,95,105.00,2000,5.00,21.00";
        let s = compute_stats(cut, "TEST", 5, &no_cpi()).unwrap();
        assert_eq!(s.p_e_min, 21.0);
        assert_eq!(s.p_e_max, 21.0);
    }

    #[test]
    fn zero_eps_rows_are_excluded_from_pe() {
        let cut = "\
date,open,high,low,close,volume,ttm_net_eps,pe_ratio
2021-01-04,100,110,90,200.00,1000,0.00,0.00
2022-06-15,105,115,95,105.00,2000,5.00,21.00";
        let s = compute_stats(cut, "TEST", 5, &no_cpi()).unwrap();
        assert_eq!(s.p_e_min, 21.0);
        assert_eq!(s.p_e_max, 21.0);
    }

    #[test]
    fn negative_pe_rows_populate_lossy_band() {
        let cut = "\
date,open,high,low,close,volume,ttm_net_eps,pe_ratio
2021-01-04,100,110,90,200.00,1000,-2.00,
2021-06-01,100,110,90,100.00,1000,-10.00,
2022-06-15,105,115,95,105.00,2000,5.00,21.00";
        let s = compute_stats(cut, "TEST", 5, &no_cpi()).unwrap();
        assert_eq!(s.p_e_min, 21.0);
        assert_eq!(s.p_e_max, 21.0);
        // max_lossy = closest to zero = 100/-10 = -10.0
        assert_eq!(s.p_e_max_lossy, Some(-10.0));
        assert_eq!(s.p_e_max_lossy_date.as_deref(), Some("2021-06-01"));
        // min_lossy = most negative = 200/-2 = -100.0
        assert_eq!(s.p_e_min_lossy, Some(-100.0));
        assert_eq!(s.p_e_min_lossy_date.as_deref(), Some("2021-01-04"));
    }

    #[test]
    fn no_lossy_pe_rows_gives_none_lossy_band() {
        let s = compute_stats(CUT, "TEST", 5, &no_cpi()).unwrap();
        assert_eq!(s.p_e_max_lossy, None);
        assert_eq!(s.p_e_min_lossy, None);
    }

    #[test]
    fn ticker_name_is_stored() {
        let s = compute_stats(CUT, "NVDA", 5, &no_cpi()).unwrap();
        assert_eq!(s.ticker, "NVDA");
    }

    #[test]
    fn mean_median_mode_positive_band() {
        let s = compute_stats(CUT, "TEST", 5, &no_cpi()).unwrap();
        // pe values: 100.0, 21.0, 60.0, 62.5
        // mean = 243.5 / 4 = 60.875
        assert_eq!(s.p_e_mean, 60.875);
        // sorted: 21, 60, 62.5, 100 → even N → (60 + 62.5) / 2 = 61.25
        assert_eq!(s.p_e_median, 61.25);
        // buckets: 100→100, 21→21, 60→60, 63→63 → all tie at 1; lowest = 21
        assert_eq!(s.p_e_mode, 21);
    }

    #[test]
    fn mode_tie_lowest_bucket_wins() {
        let cut = "\
date,open,high,low,close,volume,ttm_net_eps,pe_ratio
2021-01-01,0,0,0,30.00,0,1.00,
2021-01-02,0,0,0,30.50,0,1.00,
2021-01-03,0,0,0,50.00,0,1.00,
2021-01-04,0,0,0,50.50,0,1.00,";
        // pe values: 30.0, 30.5, 50.0, 50.5
        // buckets: 30.0→30, 30.5→31, 50.0→50, 50.5→51 → all tie; lowest = 30
        let s = compute_stats(cut, "TEST", 5, &no_cpi()).unwrap();
        assert_eq!(s.p_e_mode, 30);
    }

    #[test]
    fn mode_clear_winner() {
        let cut = "\
date,open,high,low,close,volume,ttm_net_eps,pe_ratio
2021-01-01,0,0,0,20.00,0,1.00,
2021-01-02,0,0,0,20.30,0,1.00,
2021-01-03,0,0,0,20.60,0,1.00,
2021-01-04,0,0,0,50.00,0,1.00,";
        // pe: 20.0→20, 20.3→20, 20.6→21, 50.0→50 → bucket 20 wins with count 2
        let s = compute_stats(cut, "TEST", 5, &no_cpi()).unwrap();
        assert_eq!(s.p_e_mode, 20);
    }

    #[test]
    fn no_lossy_rows_gives_none_distribution() {
        let s = compute_stats(CUT, "TEST", 5, &no_cpi()).unwrap();
        assert_eq!(s.p_e_mean_lossy, None);
        assert_eq!(s.p_e_median_lossy, None);
        assert_eq!(s.p_e_mode_lossy, None);
    }

    #[test]
    fn lossy_band_distribution() {
        let cut = "\
date,open,high,low,close,volume,ttm_net_eps,pe_ratio
2021-01-01,0,0,0,100.00,0,-2.00,
2021-01-02,0,0,0,100.00,0,-2.00,
2021-01-03,0,0,0,100.00,0,-5.00,
2022-01-01,0,0,0,100.00,0,5.00,";
        // lossy pe: -50.0, -50.0, -20.0
        // mean = -120 / 3 = -40.0
        // sorted: -50, -50, -20 → median = -50.0 (middle of 3)
        // buckets: -50→2, -20→1 → mode = -50
        let s = compute_stats(cut, "TEST", 5, &no_cpi()).unwrap();
        assert_eq!(s.p_e_mean_lossy, Some(-40.0));
        assert_eq!(s.p_e_median_lossy, Some(-50.0));
        assert_eq!(s.p_e_mode_lossy, Some(-50));
    }

    #[test]
    fn p_e_harmonic_is_inverse_of_mean_ep() {
        // CUT: pe values 100.0, 21.0, 60.0, 62.5
        // E/P values: 2/200=0.01, 5/105=0.047619, 3/180=0.016667, 4/250=0.016
        // mean(E/P) = (0.01 + 0.047619 + 0.016667 + 0.016) / 4 = 0.090286 / 4 = 0.022571
        // p_e_harmonic = 1 / 0.022571 = 44.3038
        let s = compute_stats(CUT, "TEST", 5, &no_cpi()).unwrap();
        assert_eq!(s.p_e_harmonic, Some(44.3038));
    }

    #[test]
    fn shiller_pe_uses_month_end_eps_inflation_adjusted() {
        // CUT has 4 distinct months:
        // 2021-01 eps=2 (CPI 100 → real 4.0)
        // 2022-06 eps=5 (CPI 125 → real 8.0)
        // 2023-03 eps=3 (CPI 150 → real 4.0)
        // 2024-12 eps=4 (CPI 200 → real 4.0)
        // avg_real_eps = (4+8+4+4)/4 = 5.0
        // last_price = 250.0 → Shiller P/E = 250 / 5.0 = 50.0
        let cpi = CpiData::from_map(
            [
                ((2021, 1), 100.0),
                ((2022, 6), 125.0),
                ((2023, 3), 150.0),
                ((2024, 12), 200.0),
            ]
            .into_iter()
            .collect(),
        );
        let s = compute_stats(CUT, "TEST", 5, &cpi).unwrap();
        assert_eq!(s.p_e_shiller, Some(50.0));
    }

    #[test]
    fn shiller_pe_includes_loss_months() {
        let cut = "\
date,open,high,low,close,volume,ttm_net_eps,pe_ratio
2021-01-01,0,0,0,100.00,0,-4.00,
2022-01-01,0,0,0,100.00,0,6.00,";
        // month_end_eps: (2021, 1) → -4, (2022, 1) → 6
        // avg_real_eps (no CPI adjustment) = (-4+6)/2 = 1.0
        // Shiller P/E = 100.0 / 1.0 = 100.0
        let s = compute_stats(cut, "TEST", 5, &no_cpi()).unwrap();
        assert_eq!(s.p_e_shiller, Some(100.0));
    }

    #[test]
    fn shiller_pe_uses_last_row_of_each_month() {
        let cut = "\
date,open,high,low,close,volume,ttm_net_eps,pe_ratio
2022-01-03,0,0,0,100.00,0,2.00,
2022-01-31,0,0,0,100.00,0,8.00,
2022-06-30,0,0,0,200.00,0,4.00,";
        // 2022-01 has two rows: last EPS = 8.0 (overwrites 2.0)
        // 2022-06 is a separate month in the same year: EPS = 4.0
        // avg_real_eps = (8+4)/2 = 6.0
        // Shiller P/E = 200.0 / 6.0 = 33.3333
        let s = compute_stats(cut, "TEST", 5, &no_cpi()).unwrap();
        assert_eq!(s.p_e_shiller, Some(33.3333));
    }

    #[test]
    fn shiller_pe_caps_to_n_times_12_months() {
        // Simulate a 1-year mid-month cut spanning 13 calendar months (2025-11 .. 2026-11).
        // The oldest partial boundary month (2025-11 with tiny EPS=0.10) should be dropped,
        // leaving the latest 12 months (2025-12 .. 2026-11) all with EPS=5.00.
        let mut rows = String::from("date,open,high,low,close,volume,ttm_net_eps,pe_ratio\n");
        rows.push_str("2025-11-15,0,0,0,100.00,0,0.10,\n");
        rows.push_str("2025-12-31,0,0,0,100.00,0,5.00,\n");
        for m in 1..=11 {
            rows.push_str(&format!("2026-{m:02}-15,0,0,0,100.00,0,5.00,\n"));
        }
        let s = compute_stats(&rows, "TEST", 1, &no_cpi()).unwrap();
        // Latest 12 months all have EPS=5.0 → avg_real_eps = 5.0 → CAPE = 100 / 5 = 20.0
        assert_eq!(s.p_e_shiller, Some(20.0));
    }
}
