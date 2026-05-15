// Package kelly implements the Kelly Criterion for optimal position sizing.
//
// In prediction market arbitrage the formula simplifies to:
//
//	f* = edge / odds
//	   = (p_win - p_lose) / (payoff_per_unit)
//
// For a risk-free arb (net profit is guaranteed):
//
//	f* = net_profit_pct   (edge == net profit, odds == 1)
//
// We always apply a fractional Kelly multiplier (≤ 0.25) to stay conservative.
package kelly

import "math"

const (
	// DefaultFraction is the fractional Kelly multiplier (quarter-Kelly).
	// Lower values reduce variance at the cost of long-run growth rate.
	DefaultFraction = 0.25

	// MaxBetFraction is the hard cap: never risk more than 5 % of bankroll
	// on a single trade, regardless of Kelly output.
	MaxBetFraction = 0.05

	// MinBetUSD is the minimum order size in USD we'll ever place.
	MinBetUSD = 5.0
)

// Sizer calculates Kelly-optimal position sizes.
type Sizer struct {
	Fraction    float64 // fractional Kelly multiplier
	MaxFraction float64 // hard cap per trade
	MinBetUSD   float64 // minimum trade size
}

// Default returns a Sizer with conservative defaults.
func Default() *Sizer {
	return &Sizer{
		Fraction:    DefaultFraction,
		MaxFraction: MaxBetFraction,
		MinBetUSD:   MinBetUSD,
	}
}

// Result holds position sizing output for a single opportunity.
type Result struct {
	BankrollUSD    float64 `json:"bankroll_usd"`
	KellyFull      float64 `json:"kelly_full"`       // un-fractioned Kelly %
	KellyFraction  float64 `json:"kelly_fraction"`   // after multiplier
	BetSizeUSD     float64 `json:"bet_size_usd"`     // final recommended size
	ExpectedProfitUSD float64 `json:"expected_profit_usd"`
	Viable         bool    `json:"viable"`           // false if size < MinBetUSD
}

// Size calculates the recommended bet size for a risk-free arbitrage opportunity.
//
//   - bankroll: total capital available in USD
//   - profitPct: net profit per unit (e.g. 0.04 for 4 %)
//   - buyPrice: price paid per contract (to compute expected profit in USD)
func (s *Sizer) Size(bankroll, profitPct, buyPrice float64) Result {
	if bankroll <= 0 || profitPct <= 0 || buyPrice <= 0 {
		return Result{BankrollUSD: bankroll}
	}

	// For a risk-free arb the full-Kelly fraction equals the edge directly.
	// edge = profitPct, odds = 1 (guaranteed payoff), so f* = edge/odds = profitPct.
	kellyFull := profitPct

	// Apply fractional Kelly
	kellyFrac := kellyFull * s.Fraction

	// Cap at MaxFraction
	kellyFrac = math.Min(kellyFrac, s.MaxFraction)

	betSize := bankroll * kellyFrac

	// Enforce minimum
	viable := betSize >= s.MinBetUSD

	expectedProfit := betSize * profitPct

	return Result{
		BankrollUSD:       bankroll,
		KellyFull:         kellyFull,
		KellyFraction:     kellyFrac,
		BetSizeUSD:        betSize,
		ExpectedProfitUSD: expectedProfit,
		Viable:            viable,
	}
}

// SizeContracts converts a USD bet size into a number of contracts given the
// buy price per contract.  Returns 0 if not viable.
func (s *Sizer) SizeContracts(bankroll, profitPct, buyPrice float64) (contracts float64, r Result) {
	r = s.Size(bankroll, profitPct, buyPrice)
	if !r.Viable || buyPrice <= 0 {
		return 0, r
	}
	contracts = math.Floor(r.BetSizeUSD / buyPrice)
	return contracts, r
}
