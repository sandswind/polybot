// Package model defines shared data structures across the polybot system.
package model

import "time"

// Platform identifies which prediction market platform a market belongs to.
type Platform string

const (
	PlatformPolymarket Platform = "polymarket"
	PlatformKalshi     Platform = "kalshi"
)

// Market is the normalized representation of a binary YES/NO market
// from any supported platform.
type Market struct {
	Platform  Platform  `json:"platform"`
	ID        string    `json:"id"`
	Question  string    `json:"question"`   // raw title used for fuzzy matching
	YesPrice  float64   `json:"yes_price"`  // probability [0,1] for YES outcome
	NoPrice   float64   `json:"no_price"`   // probability [0,1] for NO  outcome
	Volume    float64   `json:"volume"`     // total traded volume in USD
	FetchedAt time.Time `json:"fetched_at"`
}

// ArbitrageOpportunity represents a cross-platform pricing discrepancy.
type ArbitrageOpportunity struct {
	PolyMarket   Market  `json:"poly_market"`
	KalshiMarket Market  `json:"kalshi_market"`
	MatchScore   float64 `json:"match_score"` // fuzzy-match confidence [0,100]

	// Which side is mispriced
	Side         string  `json:"side"`          // "YES" or "NO"
	BuyPlatform  string  `json:"buy_platform"`  // buy cheap here
	SellPlatform string  `json:"sell_platform"` // sell expensive here
	BuyPrice     float64 `json:"buy_price"`
	SellPrice    float64 `json:"sell_price"`

	GrossProfit float64 `json:"gross_profit"` // sell - buy
	NetProfit   float64 `json:"net_profit"`   // after estimated fees
	ProfitPct   float64 `json:"profit_pct"`   // net / buy_price

	// Kelly position sizing (populated by the engine when bankroll is known)
	KellyContracts    float64 `json:"kelly_contracts"`     // recommended contracts to buy
	KellyBetUSD       float64 `json:"kelly_bet_usd"`       // recommended USD size
	ExpectedProfitUSD float64 `json:"expected_profit_usd"` // kelly_bet * profit_pct
}
