// Command scanner is the polybot arbitrage scanner entry point.
// It polls Polymarket and Kalshi on a configurable interval, fuzzy-matches
// equivalent markets, calculates cross-platform arbitrage opportunities with
// Kelly position sizing, and executes orders via py-clob-client.
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
	"github.com/sandswind/polybot/internal/executor"
	"github.com/sandswind/polybot/internal/fetcher"
	"github.com/sandswind/polybot/internal/matcher"
	"github.com/sandswind/polybot/internal/model"
	"github.com/sandswind/polybot/internal/notify"
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
	engCfg.Bankroll = cfg.Bankroll
	eng := engine.New(engCfg)

	// ── Order executor (optional) ─────────────────────────────────────────────
	var exec *executor.Executor
	execCfg := executor.DefaultConfig()
	execCfg.PythonBin = cfg.PythonBin
	execCfg.ScriptDir = cfg.ExecutorDir
	execCfg.DryRun = cfg.DryRun

	if e, err := executor.New(execCfg); err != nil {
		log.Printf("⚠️   Order executor unavailable (%v) — scan-only mode\n", err)
	} else if !e.IsAvailable() {
		log.Println("⚠️   python3 not found — scan-only mode")
	} else {
		exec = e
		mode := "LIVE"
		if cfg.DryRun {
			mode = "DRY-RUN"
		}
		log.Printf("✅  Order executor ready [%s]\n", mode)
	}

	// ── Lark notifier ────────────────────────────────────────────────────────
	lark := notify.NewLarkClient(cfg.LarkWebhookURL, cfg.LarkSecret)
	if lark.IsConfigured() {
		log.Println("✅  Lark notifications enabled")
	} else {
		log.Println("ℹ️   Lark notifications disabled (LARK_WEBHOOK_URL not set)")
	}

	// ── Graceful shutdown ────────────────────────────────────────────────────
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	ticker := time.NewTicker(cfg.ScanInterval)
	defer ticker.Stop()

	printBanner(cfg, exec != nil, lark.IsConfigured())

	// Run first scan immediately, then follow the ticker.
	runScan(ctx, cfg, polyClient, kalshiClient, eng, redisClient, exec, lark)

	for {
		select {
		case <-ticker.C:
			runScan(ctx, cfg, polyClient, kalshiClient, eng, redisClient, exec, lark)
		case <-quit:
			scans, arbs, _ := redisClient.GetStats(ctx)
			fmt.Printf("\n👋  Shutting down. Lifetime: %d scans, %d arb opportunities found.\n", scans, arbs)
			return
		}
	}
}

// runScan executes one full fetch → match → detect → size → execute → notify cycle.
func runScan(
	ctx context.Context,
	cfg config.Config,
	polyClient *fetcher.PolymarketClient,
	kalshiClient *fetcher.KalshiClient,
	eng *engine.Engine,
	redisClient *cache.Client,
	exec *executor.Executor,
	lark *notify.LarkClient,
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

	// ── 3. Detect arbitrage + Kelly sizing ───────────────────────────────────
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

	// ── 6. Process new opportunities ─────────────────────────────────────────
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

		// ── 7a. Lark 告警 ──────────────────────────────────────────────────
		if err := lark.SendOpportunity(ctx, opp); err != nil {
			log.Printf("⚠️   Lark notify error: %v\n", err)
		}

		// ── 7b. Execute order (buy side only — on Polymarket) ──────────────
		if exec != nil && opp.BuyPlatform == string(model.PlatformPolymarket) && opp.KellyContracts > 0 {
			placeOrder(ctx, exec, lark, opp)
		}
	}

	if newCount == 0 {
		fmt.Printf("   %d opportunity/ies found but all within cooldown window.\n", len(opps))
	}
}

// placeOrder submits a BUY limit order via the Python executor and notifies Lark.
func placeOrder(ctx context.Context, exec *executor.Executor, lark *notify.LarkClient, opp model.ArbitrageOpportunity) {
	req := executor.OrderRequest{
		MarketID: opp.PolyMarket.ID,
		Side:     "BUY",
		Outcome:  opp.Side,
		Price:    opp.BuyPrice,
		Size:     opp.KellyContracts,
	}

	result, err := exec.Execute(ctx, req)
	if err != nil {
		fmt.Printf("  │ ⚠️  Executor error: %v\n", err)
		if notifyErr := lark.SendOrderResult(ctx, opp, "", err); notifyErr != nil {
			log.Printf("⚠️   Lark order-result notify error: %v\n", notifyErr)
		}
		return
	}

	if result.DryRun {
		fmt.Printf("  │ 🧪 DRY-RUN: %s\n", result.Message)
		return
	}

	if result.OK {
		fmt.Printf("  │ ✅ Order placed — ID: %s\n", result.OrderID)
		if notifyErr := lark.SendOrderResult(ctx, opp, result.OrderID, nil); notifyErr != nil {
			log.Printf("⚠️   Lark order-result notify error: %v\n", notifyErr)
		}
	} else {
		execErr := fmt.Errorf("%s", result.Error)
		fmt.Printf("  │ ❌ Order failed: %s\n", result.Error)
		if notifyErr := lark.SendOrderResult(ctx, opp, "", execErr); notifyErr != nil {
			log.Printf("⚠️   Lark order-result notify error: %v\n", notifyErr)
		}
	}
}

// fetchWithCache returns cached markets when available, otherwise calls fetchFn.
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
	_ = redisClient.SetMarkets(ctx, key, markets)
	return markets, nil
}

// ── Pretty-print helpers ──────────────────────────────────────────────────────

func printBanner(cfg config.Config, execReady bool, larkReady bool) {
	execStatus := "✗ scan-only"
	if execReady {
		if cfg.DryRun {
			execStatus = "✓ dry-run"
		} else {
			execStatus = "✓ LIVE"
		}
	}
	larkStatus := "✗ disabled"
	if larkReady {
		larkStatus = "✓ enabled"
	}
	fmt.Println("╔══════════════════════════════════════════════════════╗")
	fmt.Println("║          polybot — Arbitrage Scanner                 ║")
	fmt.Println("╚══════════════════════════════════════════════════════╝")
	fmt.Printf("  Category    : %s\n", cfg.Category)
	fmt.Printf("  Scan every  : %s\n", cfg.ScanInterval)
	fmt.Printf("  Min profit  : %.1f%%\n", cfg.MinProfitPct*100)
	fmt.Printf("  Bankroll    : $%.2f\n", cfg.Bankroll)
	fmt.Printf("  Executor    : %s\n", execStatus)
	fmt.Printf("  Lark notify : %s\n", larkStatus)
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
	fmt.Printf("  │ Action      : BUY  %s @ $%.4f on %-12s\n",
		opp.Side, opp.BuyPrice, opp.BuyPlatform)
	fmt.Printf("  │              SELL %s @ $%.4f on %-12s\n",
		opp.Side, opp.SellPrice, opp.SellPlatform)
	fmt.Printf("  │ Net/unit    : $%.4f   Gross: $%.4f\n",
		opp.NetProfit, opp.GrossProfit)
	if opp.KellyContracts > 0 {
		fmt.Printf("  │ Kelly size  : %.0f contracts  ($%.2f)  exp. profit: $%.2f\n",
			opp.KellyContracts, opp.KellyBetUSD, opp.ExpectedProfitUSD)
	} else {
		fmt.Println("  │ Kelly size  : — (bankroll not configured or below min)")
	}
	fmt.Println("  └─────────────────────────────────────────────────────")
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
