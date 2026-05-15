package fetcher

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/sandswind/polybot/internal/model"
)

const (
	kalshiAPIBase = "https://api.elections.kalshi.com/trade-api/v2"
	// Fee estimate for Kalshi taker orders (~0.5 %)
	kalshiFeeRate = 0.005
)

// KalshiClient fetches markets from the Kalshi REST API.
// Public endpoints do not require authentication.
type KalshiClient struct {
	http    *http.Client
	baseURL string
	apiKey  string // optional; required only for order placement
}

// NewKalshiClient creates a ready-to-use Kalshi client.
// Pass an empty apiKey for read-only (scanning) usage.
func NewKalshiClient(apiKey string) *KalshiClient {
	return &KalshiClient{
		http:    &http.Client{Timeout: 10 * time.Second},
		baseURL: kalshiAPIBase,
		apiKey:  apiKey,
	}
}

// kalshiMarket is the raw JSON shape returned by the Kalshi /markets endpoint.
type kalshiMarket struct {
	Ticker     string  `json:"ticker"`
	Title      string  `json:"title"`
	Status     string  `json:"status"`
	YesBid     float64 `json:"yes_bid"`
	YesAsk     float64 `json:"yes_ask"`
	NoBid      float64 `json:"no_bid"`
	NoAsk      float64 `json:"no_ask"`
	Volume     float64 `json:"volume"`
	Category   string  `json:"category"`
}

type kalshiMarketsResp struct {
	Markets []kalshiMarket `json:"markets"`
	Cursor  string         `json:"cursor"`
}

// FetchMarkets pulls active markets from Kalshi.
// status="open" returns currently tradeable markets.
func (c *KalshiClient) FetchMarkets(ctx context.Context, category string, limit int) ([]model.Market, error) {
	url := fmt.Sprintf("%s/markets?status=open&limit=%d", c.baseURL, limit)
	if category != "" {
		url += "&category=" + category
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("kalshi: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("kalshi: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("kalshi: unexpected status %d", resp.StatusCode)
	}

	var body kalshiMarketsResp
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("kalshi: decode body: %w", err)
	}

	markets := make([]model.Market, 0, len(body.Markets))
	for _, km := range body.Markets {
		if km.Status != "open" {
			continue
		}
		// Use mid-price (avg of bid+ask) as representative price
		yesMid := midPrice(km.YesBid, km.YesAsk)
		noMid := midPrice(km.NoBid, km.NoAsk)

		// Kalshi prices are in cents [0,100]; normalise to [0,1]
		if yesMid > 1 {
			yesMid /= 100
		}
		if noMid > 1 {
			noMid /= 100
		}

		markets = append(markets, model.Market{
			Platform:  model.PlatformKalshi,
			ID:        km.Ticker,
			Question:  km.Title,
			YesPrice:  yesMid,
			NoPrice:   noMid,
			Volume:    km.Volume,
			FetchedAt: time.Now(),
		})
	}
	return markets, nil
}

// FeeRate returns the estimated taker fee rate for profit calculations.
func (c *KalshiClient) FeeRate() float64 { return kalshiFeeRate }

func midPrice(bid, ask float64) float64 {
	if bid <= 0 && ask <= 0 {
		return 0
	}
	if bid <= 0 {
		return ask
	}
	if ask <= 0 {
		return bid
	}
	return (bid + ask) / 2
}
