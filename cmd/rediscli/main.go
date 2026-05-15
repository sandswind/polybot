// Command rediscli is a polybot debug tool.
// It connects to Redis and prints a human-readable snapshot of all keys
// written by the scanner: stats, cached markets, and arb cooldown entries.
//
// Usage:
//
//	go run ./cmd/rediscli
//	go run ./cmd/rediscli -addr localhost:6379 -pattern "arb:*"
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/sandswind/polybot/config"
	"github.com/sandswind/polybot/internal/cache"
	"github.com/sandswind/polybot/internal/model"
)

func main() {
	// ── CLI flags (override env vars) ────────────────────────────────────────
	addr     := flag.String("addr",    "", "Redis address (default: REDIS_ADDR env or localhost:6379)")
	password := flag.String("password","", "Redis password")
	db       := flag.Int("db",         0,  "Redis DB number")
	pattern  := flag.String("pattern", "*", "Key glob pattern to inspect (e.g. arb:*, stats:*)")
	category := flag.String("category","sports", "Market category to read (sports|politics|crypto)")
	flag.Parse()

	cfg := config.Load()
	if *addr != "" {
		cfg.RedisAddr = *addr
	}
	if *password != "" {
		cfg.RedisPassword = *password
	}
	cfg.RedisDB = *db

	ctx := context.Background()
	c := cache.New(cfg.RedisAddr, cfg.RedisPassword, cfg.RedisDB)
	defer c.Close()

	if err := c.Ping(ctx); err != nil {
		log.Fatalf("❌  Cannot connect to Redis at %s: %v\n", cfg.RedisAddr, err)
	}
	fmt.Printf("✅  Connected to Redis at %s  (db=%d)\n\n", cfg.RedisAddr, cfg.RedisDB)

	// ── 1. Stats ─────────────────────────────────────────────────────────────
	printStats(ctx, c)

	// ── 2. Cached markets ────────────────────────────────────────────────────
	printMarkets(ctx, c, cache.KeyPolyMarkets+*category, "Polymarket")
	printMarkets(ctx, c, cache.KeyKalshiMarkets+*category, "Kalshi")

	// ── 3. Arb cooldown keys ─────────────────────────────────────────────────
	printArbKeys(ctx, c)

	// ── 4. All keys matching pattern ─────────────────────────────────────────
	if *pattern != "*" {
		printAllKeys(ctx, c, *pattern)
	}
}

// ── Section printers ─────────────────────────────────────────────────────────

func printStats(ctx context.Context, c *cache.Client) {
	scans, arbs, err := c.GetStats(ctx)
	if err != nil {
		fmt.Printf("⚠️  Stats unavailable: %v\n\n", err)
		return
	}
	fmt.Println("┌── Stats ──────────────────────────────────────────────")
	fmt.Printf("│  Total scans    : %d\n", scans)
	fmt.Printf("│  Arb found      : %d\n", arbs)
	fmt.Println("└───────────────────────────────────────────────────────")
	fmt.Println()
}

func printMarkets(ctx context.Context, c *cache.Client, key, label string) {
	ttl, _ := c.TTL(ctx, key)

	markets, err := c.GetMarkets(ctx, key)
	if err != nil {
		fmt.Printf("⚠️  %s markets read error: %v\n\n", label, err)
		return
	}
	if markets == nil {
		fmt.Printf("ℹ️   %s markets cache — MISS (key: %s)\n\n", label, key)
		return
	}

	fmt.Printf("┌── %s Markets  (key: %s  TTL: %s)\n", label, key, fmtTTL(ttl))
	fmt.Printf("│  Count: %d\n", len(markets))
	fmt.Println("│")

	// Sort by volume descending for readability
	sort.Slice(markets, func(i, j int) bool {
		return markets[i].Volume > markets[j].Volume
	})

	// Print top 10
	limit := 10
	if len(markets) < limit {
		limit = len(markets)
	}
	for i, m := range markets[:limit] {
		printMarketRow(i+1, m)
	}
	if len(markets) > 10 {
		fmt.Printf("│  … and %d more\n", len(markets)-10)
	}
	fmt.Println("└───────────────────────────────────────────────────────")
	fmt.Println()
}

func printMarketRow(n int, m model.Market) {
	age := time.Since(m.FetchedAt).Round(time.Second)
	fmt.Printf("│  %2d. [YES=%.3f NO=%.3f  vol=$%.0f  fetched %s ago]\n",
		n, m.YesPrice, m.NoPrice, m.Volume, age)
	fmt.Printf("│      %s\n", truncate(m.Question, 70))
}

func printArbKeys(ctx context.Context, c *cache.Client) {
	keys, err := c.ScanKeys(ctx, "arb:*")
	if err != nil {
		fmt.Printf("⚠️  Arb keys read error: %v\n\n", err)
		return
	}
	sort.Strings(keys)
	fmt.Printf("┌── Arb Cooldown Keys  (%d active)\n", len(keys))
	if len(keys) == 0 {
		fmt.Println("│  (none)")
	}
	for _, k := range keys {
		ttl, _ := c.TTL(ctx, k)
		parts := strings.SplitN(k, ":", 4) // arb:polyID:kalshiID:side
		side := ""
		if len(parts) == 4 {
			side = parts[3]
		}
		fmt.Printf("│  %-6s  TTL=%-8s  %s\n", side, fmtTTL(ttl), k)
	}
	fmt.Println("└───────────────────────────────────────────────────────")
	fmt.Println()
}

func printAllKeys(ctx context.Context, c *cache.Client, pattern string) {
	keys, err := c.ScanKeys(ctx, pattern)
	if err != nil {
		fmt.Printf("⚠️  Key scan error: %v\n\n", err)
		return
	}
	sort.Strings(keys)
	fmt.Printf("┌── Keys matching %q  (%d found)\n", pattern, len(keys))
	for _, k := range keys {
		typ, _ := c.KeyType(ctx, k)
		ttl, _ := c.TTL(ctx, k)
		val, _ := c.Get(ctx, k)
		display := prettyVal(val, typ)
		fmt.Printf("│  %-40s  type=%-6s  TTL=%-8s  val=%s\n",
			truncate(k, 40), typ, fmtTTL(ttl), display)
	}
	fmt.Println("└───────────────────────────────────────────────────────")

	// Dump raw JSON for debugging if only one key matched
	if len(keys) == 1 {
		fmt.Printf("\n── Raw value for %s ──\n", keys[0])
		raw, _ := c.Get(ctx, keys[0])
		if isJSON(raw) {
			var buf interface{}
			_ = json.Unmarshal([]byte(raw), &buf)
			pretty, _ := json.MarshalIndent(buf, "", "  ")
			// Print only first 2000 chars to avoid flooding terminal
			out := string(pretty)
			if len(out) > 2000 {
				out = out[:2000] + "\n… (truncated)"
			}
			fmt.Println(out)
		} else {
			fmt.Println(raw)
		}
	}

	os.Exit(0)
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func fmtTTL(d time.Duration) string {
	if d < 0 {
		return "∞"
	}
	return d.Round(time.Second).String()
}

func prettyVal(val, typ string) string {
	if typ == "string" && isJSON(val) {
		return fmt.Sprintf("<JSON len=%d>", len(val))
	}
	return truncate(val, 30)
}

func isJSON(s string) bool {
	s = strings.TrimSpace(s)
	return len(s) > 0 && (s[0] == '{' || s[0] == '[')
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
