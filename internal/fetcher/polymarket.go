// Package fetcher provides clients for each supported prediction market platform.
package fetcher

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/sandswind/polybot/internal/model"
)

const (
	gammaAPIBase = "https://gamma-api.polymarket.com"
	// Fee estimate for Polymarket taker orders (~1 %)
	polyFeeRate = 0.01
)

// PolymarketClient fetches markets from the Polymarket Gamma REST API.
// No authentication required.
type PolymarketClient struct {
	http    *http.Client
	baseURL string
}

// NewPolymarketClient creates a ready-to-use Polymarket client.
func NewPolymarketClient() *PolymarketClient {
	return &PolymarketClient{
		http:    &http.Client{Timeout: 10 * time.Second},
		baseURL: gammaAPIBase,
	}
}

// gammaMarket is the raw JSON shape returned by the Gamma API.
type gammaMarket struct {
	ID            string   `json:"id"`
	Question      string   `json:"question"`
	Category      string   `json:"category"`
	Active        bool     `json:"active"`
	OutcomePrices []string `json:"outcomePrices"`
	Volume        string   `json:"volume"`
}

// FetchMarkets pulls active markets from Polymarket.
// category examples: "sports", "politics", "crypto"
func (c *PolymarketClient) FetchMarkets(ctx context.Context, category string, limit int) ([]model.Market, error) {
	url := fmt.Sprintf("%s/markets?active=true&tag_slug=%s&limit=%d", c.baseURL, category, limit)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("polymarket: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("polymarket: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("polymarket: unexpected status %d", resp.StatusCode)
	}

	// The API may return a plain array or {"data": [...]}
	var raw json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("polymarket: decode body: %w", err)
	}

	var items []gammaMarket
	// try plain array first
	if err := json.Unmarshal(raw, &items); err != nil {
		// fallback: wrapped object
		var wrapper struct {
			Data []gammaMarket `json:"data"`
		}
		if err2 := json.Unmarshal(raw, &wrapper); err2 != nil {
			return nil, fmt.Errorf("polymarket: parse markets: %w", err)
		}
		items = wrapper.Data
	}

	markets := make([]model.Market, 0, len(items))
	for _, it := range items {
		m := normalize(it)
		if m.YesPrice > 0 {
			markets = append(markets, m)
		}
	}
	return markets, nil
}

// FeeRate returns the estimated taker fee rate for profit calculations.
func (c *PolymarketClient) FeeRate() float64 { return polyFeeRate }

// normalize converts a raw Gamma API market into our internal model.
func normalize(raw gammaMarket) model.Market {
	var yesPrice, noPrice float64

	if len(raw.OutcomePrices) >= 2 {
		yesPrice, _ = strconv.ParseFloat(raw.OutcomePrices[0], 64)
		noPrice, _ = strconv.ParseFloat(raw.OutcomePrices[1], 64)
	} else if len(raw.OutcomePrices) == 1 {
		yesPrice, _ = strconv.ParseFloat(raw.OutcomePrices[0], 64)
		noPrice = 1 - yesPrice
	}

	vol, _ := strconv.ParseFloat(raw.Volume, 64)

	return model.Market{
		Platform:  model.PlatformPolymarket,
		ID:        raw.ID,
		Question:  raw.Question,
		YesPrice:  yesPrice,
		NoPrice:   noPrice,
		Volume:    vol,
		FetchedAt: time.Now(),
	}
}
