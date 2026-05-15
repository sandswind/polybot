// Package model — order types used by the buy-signal queue.
package model

import "time"

// BuySignal is the message pushed into the Redis buy queue by the scanner
// and consumed by the buyer worker.
//
// Redis key: BuyQueueKey  (LPUSH / BLPOP on a List)
type BuySignal struct {
	// ── Market identification ─────────────────────────────────────────────
	MarketID string `json:"market_id"` // Polymarket token / condition ID
	Question string `json:"question"`  // human-readable label for logging

	// ── Trade parameters ─────────────────────────────────────────────────
	Outcome string  `json:"outcome"`  // "YES" | "NO"
	Price   float64 `json:"price"`    // limit price [0,1]
	Size    float64 `json:"size"`     // contracts (USDC shares), already Kelly-sized

	// ── Context (for Lark notifications) ─────────────────────────────────
	ProfitPct         float64 `json:"profit_pct"`
	ExpectedProfitUSD float64 `json:"expected_profit_usd"`
	MatchScore        float64 `json:"match_score"`

	// ── Metadata ─────────────────────────────────────────────────────────
	Source    string    `json:"source"`     // e.g. "arbitrage-engine"
	EnqueueAt time.Time `json:"enqueue_at"` // when the signal was pushed
}

// BuyResult is stored back into Redis after an order attempt.
type BuyResult struct {
	Signal    BuySignal `json:"signal"`
	OrderID   string    `json:"order_id"`
	OK        bool      `json:"ok"`
	Error     string    `json:"error,omitempty"`
	ExecutedAt time.Time `json:"executed_at"`
}
