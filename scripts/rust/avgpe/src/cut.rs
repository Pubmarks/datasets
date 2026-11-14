use std::error::Error;
use std::io::Cursor;

use chrono::{Datelike, Local};

pub fn cut_to_last_n_years(combined: &str, years: u32) -> Result<String, Box<dyn Error>> {
    let today = Local::now().date_naive();
    let cutoff_year = today.year() - years as i32;
    // chrono handles the Feb-29 edge case: falls back to Feb-28 on non-leap years
    // Feb-29 on a non-leap target year → fall back to Feb-28
    let cutoff = today
        .with_year(cutoff_year)
        .unwrap_or_else(|| {
            today
                .with_day(28)
                .and_then(|d| d.with_year(cutoff_year))
                .expect("invalid cutoff date")
        });
    let cutoff_str = cutoff.format("%Y-%m-%d").to_string();

    let mut reader = csv::Reader::from_reader(Cursor::new(combined));
    let headers = reader.headers()?.clone();

    let mut out = Vec::new();
    let mut writer = csv::Writer::from_writer(&mut out);
    writer.write_record(&headers)?;

    for result in reader.records() {
        let record = result?;
        if let Some(date) = record.get(0)
            && date >= cutoff_str.as_str()
        {
            writer.write_record(&record)?;
        }
    }

    writer.flush()?;
    drop(writer);
    Ok(String::from_utf8(out)?)
}

#[cfg(test)]
mod tests {
    use super::*;

    const COMBINED: &str = "\
date,open,high,low,close,volume,ttm_net_eps,pe_ratio
2010-01-01,100,110,90,200.00,1000,4.00,50.00
2015-06-15,105,115,95,210.00,2000,4.50,46.67
2020-03-20,90,100,80,180.00,3000,3.50,51.43
2023-12-31,120,130,110,250.00,4000,5.00,50.00";

    #[test]
    fn rows_within_range_are_kept() {
        // using a very large year window — all rows should be kept
        let out = cut_to_last_n_years(COMBINED, 100).unwrap();
        let row_count = out.trim().lines().count();
        assert_eq!(row_count, 5, "header + 4 data rows: {out}");
    }

    #[test]
    fn rows_before_cutoff_are_dropped() {
        // 5 years back from 2026-05-14 → cutoff 2021-05-14; only 2023-12-31 qualifies
        let out = cut_to_last_n_years(COMBINED, 5).unwrap();
        let lines: Vec<&str> = out.trim().lines().collect();
        assert_eq!(lines.len(), 2, "header + 1 row: {out}");
        assert!(
            lines[1].starts_with("2023-12-31"),
            "wrong row kept: {}",
            lines[1]
        );
    }

    #[test]
    fn header_is_always_preserved() {
        let out = cut_to_last_n_years(COMBINED, 0).unwrap();
        let first = out.trim().lines().next().unwrap();
        assert!(first.starts_with("date,"), "header missing: {first}");
    }

    #[test]
    fn rows_with_empty_pe_ratio_within_range_are_kept() {
        let combined = "\
date,open,high,low,close,volume,ttm_net_eps,pe_ratio
2024-01-01,100,110,90,200.00,1000,,";
        let out = cut_to_last_n_years(combined, 5).unwrap();
        let lines: Vec<&str> = out.trim().lines().collect();
        assert_eq!(
            lines.len(),
            2,
            "row with empty pe_ratio should be kept: {out}"
        );
    }
}
