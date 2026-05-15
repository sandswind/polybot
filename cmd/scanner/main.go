// Command scanner is the polybot arbitrage scanner entry point.
// It polls Polymarket and Kalshi on a configurable interval, fuzzy-matches
// equivalent markets, calculates cross-platform arbitrage opportunities, and
// prints a rich terminal report while deduplicating alerts via Redis.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/sandswind/polybot/config"
	"github.com/sandswind/polybot/internal/cache"
	"github.com/sandswind/polybot/internal/engine"
	"github.com/sandswind/polybot/internal/fetcher"
	"github.com/sandswind/polybot/internal/matcher"
	"github.com/sandswind/polybot/internal/model"
)

func main() {
	cfg := config.Load()

	// ── Redis ────────────────────────────────────────────────────────────────
	redisClient := cache.New(cfg.RedisAddr, cfg.RedisPassword, cfg.RedisDB)
	ctx := context.Background()
	if err := redisClient.Ping(ctx); err != nil {
		log.Fatalf("❌  Redis connection failed (%s): %v\n", cfg.RedisAddr, err)
	}
	log.Printf("✅  Redis connected at %s\n", cfg.RedisAddr)
	defer redisClient.Close()

	// ── Platform clients ─────────────────────────────────────────────────────
	polyClient := fetcher.NewPolymarketClient()
	kalshiClient := fetcher.NewKalshiClient(cfg.KalshiAPIKey)

	// ── Arbitrage engine ─────────────────────────────────────────────────────
	engCfg := engine.DefaultConfig()
	engCfg.MinProfitPct = cfg.MinProfitPct
	engCfg.PolyFeeRate = polyClient.FeeRate()
	engCfg.KalshiFeeRate = kalshiClient.FeeRate()
	eng := engine.New(engCfg)

	// ── Graceful shutdown ────────────────────────────────────────────────────
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	ticker := time.NewTicker(cfg.ScanInterval)
	defer ticker.Stop()

	printBanner(cfg)

	// Run first scan immediately, then follow the ticker.
	runScan(ctx, cfg, polyClient, kalshiClient, eng, redisClient)

	for {
		select {
		case <-ticker.C:
			runScan(ctx, cfg, polyClient, kalshiClient, eng, redisClient)
		case <-quit:
			scans, arbs, _ := redisClient.GetStats(ctx)
			fmt.Printf("\n👋  Shutting down. Lifetime: %d scans, %d arb opportunities found.\n", scans, arbs)
			return
		}
	}
}

// runScan executes one full fetch → match → detect → report cycle.
func runScan(
	ctx context.Context,
	cfg config.Config,
	polyClient *fetcher.PolymarketClient,
	kalshiClient *fetcher.KalshiClient,
	eng *engine.Engine,
	redisClient *cache.Client,
) {
	scanStart := time.Now()

	// ── 1. Fetch (with Redis cache) ──────────────────────────────────────────
	polyMarkets, err := fetchWithCache(ctx, redisClient, "poly:markets:"+cfg.Category,
		func() ([]model.Market, error) {
			return polyClient.FetchMarkets(ctx, cfg.Category, cfg.FetchLimit)
		})
	if err != nil {
		log.Printf("⚠️   Polymarket fetch error: %v\n", err)
		return
	}

	kalshiMarkets, err := fetchWithCache(ctx, redisClient, "kalshi:markets:"+cfg.Category,
		func() ([]model.Market, error) {
			return kalshiClient.FetchMarkets(ctx, cfg.Category, cfg.FetchLimit)
		})
	if err != nil {
		log.Printf("⚠️   Kalshi fetch error: %v\n", err)
		return
	}

	// ── 2. Fuzzy-match ───────────────────────────────────────────────────────
	pairs := matcher.Match(polyMarkets, kalshiMarkets)

	// ── 3. Detect arbitrage ──────────────────────────────────────────────────
	opps := eng.Scan(pairs)

	// ── 4. Update stats ──────────────────────────────────────────────────────
	scanCount, _ := redisClient.IncrScanCount(ctx)

	// ── 5. Print summary ─────────────────────────────────────────────────────
	elapsed := time.Since(scanStart).Round(time.Millisecond)
	fmt.Printf("\n%s  Scan #%d  |  Poly: %d  Kalshi: %d  Pairs: %d  Elapsed: %s\n",
		time.Now().Format("15:04:05"),
		scanCount,
		len(polyMarkets), len(kalshiMarkets),
		len(pairs), elapsed,
	)

	if len(opps) == 0 {
		fmt.Println("   No opportunities above threshold.")
		return
	}

	// ── 6. Report new opportunities ──────────────────────────────────────────
	newCount := 0
	for _, opp := range opps {
		isNew, err := redisClient.IsNewOpportunity(ctx,
			opp.PolyMarket.ID, opp.KalshiMarket.ID, opp.Side)
		if err != nil {
			log.Printf("⚠️   Redis dedup error: %v\n", err)
		}
		if !isNew {
			continue
		}
		redisClient.IncrArbFound(ctx)
		newCount++
		printOpportunity(opp)
	}

	if newCount == 0 {
		fmt.Printf("   %d opportunity/ies found but all within cooldown window.\n", len(opps))
	}
}

// fetchWithCache returns cached markets when available, otherwise calls fetchFn
// and stores the result in Redis.
func fetchWithCache(
	ctx context.Context,
	redisClient *cache.Client,
	key string,
	fetchFn func() ([]model.Market, error),
) ([]model.Market, error) {
	if cached, err := redisClient.GetMarkets(ctx, key); err == nil && cached != nil {
		return cached, nil
	}
	markets, err := fetchFn()
	if err != nil {
		return nil, err
	}
	_ = redisClient.SetMarkets(ctx, key, markets) // best-effort cache write
	return markets, nil
}

// ── Pretty-print helpers ──────────────────────────────────────────────────────

func printBanner(cfg config.Config) {
	fmt.Println("╔══════════════════════════════════════════════════════╗")
	fmt.Println("║          polybot — Arbitrage Scanner MVP             ║")
	fmt.Println("╚══════════════════════════════════════════════════════╝")
	fmt.Printf("  Category    : %s\n", cfg.Category)
	fmt.Printf("  Scan every  : %s\n", cfg.ScanInterval)
	fmt.Printf("  Min profit  : %.1f%%\n", cfg.MinProfitPct*100)
	fmt.Printf("  Redis       : %s\n", cfg.RedisAddr)
	fmt.Println()
}

func printOpportunity(opp model.ArbitrageOpportunity) {
	fmt.Println("  ┌─────────────────────────────────────────────────────")
	fmt.Printf("  │ 🎯  ARB FOUND  [%s side]  profit: %.2f%%\n",
		opp.Side, opp.ProfitPct*100)
	fmt.Printf("  │ Match score : %.0f/100\n", opp.MatchScore)
	fmt.Printf("  │ Polymarket  : %s\n", truncate(opp.PolyMarket.Question, 60))
	fmt.Printf("  │ Kalshi      : %s\n", truncate(opp.KalshiMarket.Question, 60))
	fmt.Printf("  │ Action      : BUY %s @ $%.4f on %-12s\n",
		opp.Side, opp.BuyPrice, opp.BuyPlatform)
	fmt.Printf("  │              SELL %s @ $%.4f on %-12s\n",
		opp.Side, opp.SellPrice, opp.SellPlatform)
	fmt.Printf("  │ Net/unit    : $%.4f   Gross: $%.4f\n",
		opp.NetProfit, opp.GrossProfit)
	fmt.Println("  └─────────────────────────────────────────────────────")
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
