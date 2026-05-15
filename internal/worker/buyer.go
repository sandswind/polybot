// Package worker provides the buy-signal consumer that pops from Redis and
// executes orders via the Python py-clob-client executor.
//
// Flow:
//
//	Redis List "queue:buy"
//	        │
//	   BLPOP (blocks, 5 s timeout, then loops)
//	        │
//	  BuySignal decoded
//	        │
//	  executor.Execute()   ← py-clob-client
//	        │
//	  SaveBuyResult → Redis "queue:buy:results"
//	        │
//	  lark.SendOrderResult
package worker

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/sandswind/polybot/internal/cache"
	"github.com/sandswind/polybot/internal/executor"
	"github.com/sandswind/polybot/internal/model"
	"github.com/sandswind/polybot/internal/notify"
)

const popTimeout = 5 * time.Second // BLPOP block duration per iteration

// Buyer is a long-running worker that reads BuySignals from a Redis queue
// and executes them as Polymarket limit orders.
type Buyer struct {
	redis    *cache.Client
	exec     *executor.Executor // nil → dry-run log only
	lark     *notify.LarkClient
	dryRun   bool
	workerID string
}

// NewBuyer creates a Buyer.
// exec may be nil — in that case signals are logged but not executed.
func NewBuyer(
	redisClient *cache.Client,
	exec *executor.Executor,
	lark *notify.LarkClient,
	dryRun bool,
	workerID string,
) *Buyer {
	return &Buyer{
		redis:    redisClient,
		exec:     exec,
		lark:     lark,
		dryRun:   dryRun,
		workerID: workerID,
	}
}

// Run blocks and processes signals until ctx is cancelled.
// Call it in a goroutine:  go buyer.Run(ctx)
func (b *Buyer) Run(ctx context.Context) {
	log.Printf("[buyer-%s] 🟢 started (queue: %s  dry_run: %v)\n",
		b.workerID, cache.BuyQueueKey, b.dryRun)

	for {
		select {
		case <-ctx.Done():
			log.Printf("[buyer-%s] 🔴 stopped\n", b.workerID)
			return
		default:
		}

		// ── BLPOP — blocks up to popTimeout ─────────────────────────────────
		sig, err := b.redis.PopBuySignal(ctx, popTimeout)
		if err != nil {
			// Context cancelled during BLPOP — normal shutdown path
			if ctx.Err() != nil {
				return
			}
			log.Printf("[buyer-%s] ⚠️  pop error: %v\n", b.workerID, err)
			time.Sleep(2 * time.Second) // brief back-off on Redis errors
			continue
		}
		if sig == nil {
			// Timeout — queue empty, loop back
			continue
		}

		b.process(ctx, *sig)
	}
}

// process handles a single BuySignal.
func (b *Buyer) process(ctx context.Context, sig model.BuySignal) {
	lag := time.Since(sig.EnqueueAt).Round(time.Millisecond)
	log.Printf("[buyer-%s] 📥 signal  market=%s  outcome=%s  price=%.4f  size=%.0f  lag=%s\n",
		b.workerID, truncate(sig.Question, 50), sig.Outcome, sig.Price, sig.Size, lag)

	result := model.BuyResult{
		Signal:     sig,
		ExecutedAt: time.Now(),
	}

	// ── Dry-run path ─────────────────────────────────────────────────────────
	if b.dryRun || b.exec == nil {
		msg := fmt.Sprintf("DRY-RUN: would BUY %.0f × %s @ $%.4f on market %s",
			sig.Size, sig.Outcome, sig.Price, sig.MarketID)
		log.Printf("[buyer-%s] 🧪 %s\n", b.workerID, msg)
		result.OK = true
		result.OrderID = "dry-run"
		b.save(ctx, result)
		return
	}

	// ── Live order ───────────────────────────────────────────────────────────
	req := executor.OrderRequest{
		MarketID: sig.MarketID,
		Side:     "BUY",
		Outcome:  sig.Outcome,
		Price:    sig.Price,
		Size:     sig.Size,
	}

	execResult, err := b.exec.Execute(ctx, req)
	if err != nil {
		log.Printf("[buyer-%s] ❌ executor error: %v\n", b.workerID, err)
		result.OK = false
		result.Error = err.Error()
		b.save(ctx, result)
		b.notifyResult(ctx, sig, result)
		return
	}

	if execResult.OK {
		log.Printf("[buyer-%s] ✅ order placed  ID=%s\n", b.workerID, execResult.OrderID)
		result.OK = true
		result.OrderID = execResult.OrderID
	} else {
		log.Printf("[buyer-%s] ❌ order rejected: %s\n", b.workerID, execResult.Error)
		result.OK = false
		result.Error = execResult.Error
	}

	b.save(ctx, result)
	b.notifyResult(ctx, sig, result)
}

// save persists the result to Redis.
func (b *Buyer) save(ctx context.Context, r model.BuyResult) {
	if err := b.redis.SaveBuyResult(ctx, r); err != nil {
		log.Printf("[buyer-%s] ⚠️  save result error: %v\n", b.workerID, err)
	}
}

// notifyResult sends a Lark message with the order outcome.
func (b *Buyer) notifyResult(ctx context.Context, sig model.BuySignal, result model.BuyResult) {
	if !b.lark.IsConfigured() {
		return
	}

	// Construct a minimal ArbitrageOpportunity for reuse of existing Lark helper
	opp := model.ArbitrageOpportunity{
		PolyMarket: model.Market{
			ID:       sig.MarketID,
			Question: sig.Question,
		},
		Side:              sig.Outcome,
		BuyPrice:          sig.Price,
		ProfitPct:         sig.ProfitPct,
		KellyContracts:    sig.Size,
		KellyBetUSD:       sig.Size * sig.Price,
		ExpectedProfitUSD: sig.ExpectedProfitUSD,
	}

	var execErr error
	if !result.OK {
		execErr = fmt.Errorf("%s", result.Error)
	}
	if err := b.lark.SendOrderResult(ctx, opp, result.OrderID, execErr); err != nil {
		log.Printf("[buyer-%s] ⚠️  lark notify error: %v\n", b.workerID, err)
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
