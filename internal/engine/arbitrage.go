// Package engine implements the cross-platform arbitrage detection logic.
package engine

import (
	"github.com/sandswind/polybot/internal/kelly"
	"github.com/sandswind/polybot/internal/matcher"
	"github.com/sandswind/polybot/internal/model"
)

// Config holds tunable parameters for the arbitrage engine.
type Config struct {
	// MinProfitPct is the minimum net profit percentage to surface an opportunity.
	// e.g. 0.02 = 2 %
	MinProfitPct float64

	// PolyFeeRate and KalshiFeeRate are the estimated taker fee rates per platform.
	PolyFeeRate   float64
	KalshiFeeRate float64

	// Bankroll is the total available capital in USD used for Kelly sizing.
	// Set to 0 to skip Kelly calculation.
	Bankroll float64
}

// DefaultConfig returns conservative defaults.
func DefaultConfig() Config {
	return Config{
		MinProfitPct:  0.02,
		PolyFeeRate:   0.01,
		KalshiFeeRate: 0.005,
		Bankroll:      0, // must be set explicitly to enable Kelly sizing
	}
}

// Engine detects arbitrage opportunities between matched market pairs.
type Engine struct {
	cfg    Config
	sizer  *kelly.Sizer
}

// New creates a new Engine with the given config.
func New(cfg Config) *Engine {
	return &Engine{
		cfg:   cfg,
		sizer: kelly.Default(),
	}
}

// Scan accepts matched market pairs and returns all opportunities that exceed
// the configured minimum profit threshold, with Kelly sizing applied.
//
// Arbitrage logic (binary YES/NO contracts):
//
//	If Kalshi YES price < Polymarket YES price:
//	  → buy YES on Kalshi, the position is worth $1 if event happens
//	  → the equivalent position on Polymarket is priced higher (you "sold" at that price)
//	  → profit = poly_yes_price - kalshi_yes_price - fees
//
//	Same logic applies for the NO side.
func (e *Engine) Scan(pairs []matcher.MarketPair) []model.ArbitrageOpportunity {
	var opps []model.ArbitrageOpportunity

	for _, p := range pairs {
		// --- YES side ---
		if opp, ok := e.calcOpportunity(p, "YES",
			p.Kalshi.YesPrice, p.Poly.YesPrice,
			string(model.PlatformKalshi), string(model.PlatformPolymarket),
		); ok {
			opp.MatchScore = p.Score
			e.applyKelly(&opp)
			opps = append(opps, opp)
		} else if opp, ok := e.calcOpportunity(p, "YES",
			p.Poly.YesPrice, p.Kalshi.YesPrice,
			string(model.PlatformPolymarket), string(model.PlatformKalshi),
		); ok {
			opp.MatchScore = p.Score
			e.applyKelly(&opp)
			opps = append(opps, opp)
		}

		// --- NO side ---
		if opp, ok := e.calcOpportunity(p, "NO",
			p.Kalshi.NoPrice, p.Poly.NoPrice,
			string(model.PlatformKalshi), string(model.PlatformPolymarket),
		); ok {
			opp.MatchScore = p.Score
			e.applyKelly(&opp)
			opps = append(opps, opp)
		} else if opp, ok := e.calcOpportunity(p, "NO",
			p.Poly.NoPrice, p.Kalshi.NoPrice,
			string(model.PlatformPolymarket), string(model.PlatformKalshi),
		); ok {
			opp.MatchScore = p.Score
			e.applyKelly(&opp)
			opps = append(opps, opp)
		}
	}
	return opps
}

// applyKelly fills Kelly sizing fields on an opportunity in-place.
// No-op when bankroll is not configured.
func (e *Engine) applyKelly(opp *model.ArbitrageOpportunity) {
	if e.cfg.Bankroll <= 0 {
		return
	}
	contracts, r := e.sizer.SizeContracts(e.cfg.Bankroll, opp.ProfitPct, opp.BuyPrice)
	opp.KellyContracts = contracts
	opp.KellyBetUSD = r.BetSizeUSD
	opp.ExpectedProfitUSD = r.ExpectedProfitUSD
}

// calcOpportunity evaluates one directional trade: buy at buyPrice, sell at sellPrice.
// Returns (opportunity, true) only when net profit exceeds the threshold.
func (e *Engine) calcOpportunity(
	p matcher.MarketPair,
	side string,
	buyPrice, sellPrice float64,
	buyPlatform, sellPlatform string,
) (model.ArbitrageOpportunity, bool) {
	if buyPrice <= 0 || sellPrice <= 0 {
		return model.ArbitrageOpportunity{}, false
	}

	gross := sellPrice - buyPrice
	if gross <= 0 {
		return model.ArbitrageOpportunity{}, false
	}

	// Total fee: buy-side fee + sell-side fee (both applied to their respective prices)
	fees := buyPrice*e.cfg.PolyFeeRate + sellPrice*e.cfg.KalshiFeeRate
	if buyPlatform == string(model.PlatformPolymarket) {
		fees = buyPrice*e.cfg.PolyFeeRate + sellPrice*e.cfg.KalshiFeeRate
	} else {
		fees = buyPrice*e.cfg.KalshiFeeRate + sellPrice*e.cfg.PolyFeeRate
	}

	net := gross - fees
	if net <= 0 {
		return model.ArbitrageOpportunity{}, false
	}

	pct := net / buyPrice
	if pct < e.cfg.MinProfitPct {
		return model.ArbitrageOpportunity{}, false
	}

	return model.ArbitrageOpportunity{
		PolyMarket:   p.Poly,
		KalshiMarket: p.Kalshi,
		Side:         side,
		BuyPlatform:  buyPlatform,
		SellPlatform: sellPlatform,
		BuyPrice:     buyPrice,
		SellPrice:    sellPrice,
		GrossProfit:  gross,
		NetProfit:    net,
		ProfitPct:    pct,
	}, true
}
