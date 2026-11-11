package main

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
)

const (
	chartIframePath = "https://www.macrotrends.net/production/stocks/desktop/PRODUCTION/stock_price_history.php"
	downloadBase    = "https://www.macrotrends.net/assets/php/stock_data_download.php"
)

var stockDataDownloadRE = regexp.MustCompile(`stock_data_download\.php\?s=([^&']+)&t=([^']+)`)

func fetchHTML(ctx context.Context, urlStr string) (string, error) {
	body, _, err := zenrowsGet(ctx, urlStr)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// chartIframeURL builds the chart iframe URL. If yearsBack is nil, the yb query param is omitted (MacroTrends default range).
func chartIframeURL(symbol string, yearsBack *int) string {
	sym := normalizeSymbol(symbol)
	q := url.Values{}
	q.Set("t", sym)
	if yearsBack != nil {
		q.Set("yb", fmt.Sprintf("%d", *yearsBack))
	}
	return chartIframePath + "?" + q.Encode()
}

func downloadURLFromChartHTML(chartHTML string) (string, error) {
	m := stockDataDownloadRE.FindStringSubmatch(chartHTML)
	if len(m) != 3 {
		return "", fmt.Errorf("stock_data_download URL not found in chart HTML")
	}
	u, err := url.Parse(downloadBase)
	if err != nil {
		return "", err
	}
	q := u.Query()
	q.Set("s", m[1])
	q.Set("t", m[2])
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func fetchOHLCVCSV(ctx context.Context, symbol string, yearsBack *int) ([]byte, error) {
	chartURL := chartIframeURL(symbol, yearsBack)
	html, err := fetchHTML(ctx, chartURL)
	if err != nil {
		return nil, err
	}
	dl, err := downloadURLFromChartHTML(html)
	if err != nil {
		return nil, err
	}
	body, _, err := zenrowsGet(ctx, dl)
	if err != nil {
		return nil, err
	}
	return body, nil
}
