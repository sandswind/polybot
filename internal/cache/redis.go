// Package cache wraps go-redis to store fetched markets and
// deduplicate arbitrage alerts so we don't fire the same alert repeatedly.
package cache

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/sandswind/polybot/internal/model"
)

const (
	// How long raw market snapshots are cached.
	marketTTL = 60 * time.Second
	// How long an arb opportunity is suppressed after first alert.
	alertCooldown = 5 * time.Minute
)

// ─── Key helpers ─────────────────────────────────────────────────────────────

const (
	KeyPolyMarkets   = "poly:markets:"    // + category suffix
	KeyKalshiMarkets = "kalshi:markets:"  // + category suffix
	KeyStatScans     = "stats:scans"
	KeyStatArbFound  = "stats:arb_found"
)

// Client is a thin Redis wrapper used by the scanner.
type Client struct {
	rdb *redis.Client
}

// New creates a Client connected to the given Redis address (e.g. "localhost:6379").
func New(addr, password string, db int) *Client {
	rdb := redis.NewClient(&redis.Options{
		Addr:     addr,
		Password: password,
		DB:       db,
	})
	return &Client{rdb: rdb}
}

// Ping verifies the Redis connection is healthy.
func (c *Client) Ping(ctx context.Context) error {
	return c.rdb.Ping(ctx).Err()
}

// Close releases the underlying connection pool.
func (c *Client) Close() error {
	return c.rdb.Close()
}

// ─── Market cache ────────────────────────────────────────────────────────────

// SetMarkets serialises a slice of markets and stores it under the given key.
func (c *Client) SetMarkets(ctx context.Context, key string, markets []model.Market) error {
	data, err := json.Marshal(markets)
	if err != nil {
		return fmt.Errorf("cache: marshal markets: %w", err)
	}
	return c.rdb.Set(ctx, key, data, marketTTL).Err()
}

// GetMarkets retrieves and deserialises markets stored under key.
// Returns (nil, nil) on a cache miss.
func (c *Client) GetMarkets(ctx context.Context, key string) ([]model.Market, error) {
	data, err := c.rdb.Get(ctx, key).Bytes()
	if err == redis.Nil {
		return nil, nil // cache miss
	}
	if err != nil {
		return nil, fmt.Errorf("cache: get markets: %w", err)
	}
	var markets []model.Market
	if err := json.Unmarshal(data, &markets); err != nil {
		return nil, fmt.Errorf("cache: unmarshal markets: %w", err)
	}
	return markets, nil
}

// ─── Arbitrage alert deduplication ──────────────────────────────────────────

// arbKey builds a deterministic Redis key for an opportunity pair.
func arbKey(polyID, kalshiID, side string) string {
	return fmt.Sprintf("arb:%s:%s:%s", polyID, kalshiID, side)
}

// IsNewOpportunity returns true only if this (polyID, kalshiID, side) triple
// has not been seen within the alertCooldown window.
// If it is new, the key is stamped so future calls return false.
func (c *Client) IsNewOpportunity(ctx context.Context, polyID, kalshiID, side string) (bool, error) {
	key := arbKey(polyID, kalshiID, side)
	set, err := c.rdb.SetNX(ctx, key, "1", alertCooldown).Result()
	if err != nil {
		return false, fmt.Errorf("cache: setnx arb key: %w", err)
	}
	return set, nil
}

// ─── Stats ───────────────────────────────────────────────────────────────────

// IncrScanCount atomically increments a lifetime scan counter.
func (c *Client) IncrScanCount(ctx context.Context) (int64, error) {
	return c.rdb.Incr(ctx, "stats:scans").Result()
}

// IncrArbFound atomically increments a lifetime arb-found counter.
func (c *Client) IncrArbFound(ctx context.Context) (int64, error) {
	return c.rdb.Incr(ctx, "stats:arb_found").Result()
}

// GetStats returns lifetime scan and arb-found counts.
func (c *Client) GetStats(ctx context.Context) (scans, arbFound int64, err error) {
	pipe := c.rdb.Pipeline()
	scanCmd := pipe.Get(ctx, "stats:scans")
	arbCmd := pipe.Get(ctx, "stats:arb_found")
	if _, err = pipe.Exec(ctx); err != nil && err != redis.Nil {
		return 0, 0, fmt.Errorf("cache: get stats: %w", err)
	}
	scans, _ = scanCmd.Int64()
	arbFound, _ = arbCmd.Int64()
	return scans, arbFound, nil
}

// ─── Buy-signal queue (Redis List) ───────────────────────────────────────────
//
// Architecture:
//   scanner  →  LPUSH queue:buy  →  Redis List  →  BLPOP  →  buyer worker
//
// The queue is a simple FIFO: scanner pushes to the HEAD (LPUSH),
// worker pops from the TAIL (BRPOP) — oldest signal first.

const (
	// BuyQueueKey is the Redis List key for pending buy signals.
	BuyQueueKey = "queue:buy"
	// BuyResultKey is a Redis List that stores completed order results (capped).
	BuyResultKey   = "queue:buy:results"
	BuyResultCap   = 500 // keep last N results
)

// PushBuySignal serialises sig and pushes it to the buy queue (LPUSH).
func (c *Client) PushBuySignal(ctx context.Context, sig model.BuySignal) error {
	data, err := json.Marshal(sig)
	if err != nil {
		return fmt.Errorf("cache: marshal buy signal: %w", err)
	}
	return c.rdb.LPush(ctx, BuyQueueKey, data).Err()
}

// PopBuySignal blocks for up to timeout waiting for the next signal (BRPOP).
// Returns (nil, nil) on timeout.
func (c *Client) PopBuySignal(ctx context.Context, timeout time.Duration) (*model.BuySignal, error) {
	// BRPOP returns [key, value] — we pop from the TAIL so oldest first.
	res, err := c.rdb.BRPop(ctx, timeout, BuyQueueKey).Result()
	if err == redis.Nil {
		return nil, nil // timeout — no item available
	}
	if err != nil {
		return nil, fmt.Errorf("cache: brpop buy queue: %w", err)
	}
	// res[0] = key name, res[1] = JSON value
	var sig model.BuySignal
	if err := json.Unmarshal([]byte(res[1]), &sig); err != nil {
		return nil, fmt.Errorf("cache: unmarshal buy signal: %w", err)
	}
	return &sig, nil
}

// QueueLength returns how many signals are waiting in the buy queue.
func (c *Client) QueueLength(ctx context.Context) (int64, error) {
	return c.rdb.LLen(ctx, BuyQueueKey).Result()
}

// PeekBuyQueue returns up to n signals from the queue without removing them.
// Useful for the rediscli debug tool.
func (c *Client) PeekBuyQueue(ctx context.Context, n int64) ([]model.BuySignal, error) {
	items, err := c.rdb.LRange(ctx, BuyQueueKey, 0, n-1).Result()
	if err != nil {
		return nil, fmt.Errorf("cache: lrange buy queue: %w", err)
	}
	sigs := make([]model.BuySignal, 0, len(items))
	for _, item := range items {
		var sig model.BuySignal
		if err := json.Unmarshal([]byte(item), &sig); err == nil {
			sigs = append(sigs, sig)
		}
	}
	return sigs, nil
}

// SaveBuyResult prepends a result to the results list and trims to cap.
func (c *Client) SaveBuyResult(ctx context.Context, result model.BuyResult) error {
	data, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("cache: marshal buy result: %w", err)
	}
	pipe := c.rdb.Pipeline()
	pipe.LPush(ctx, BuyResultKey, data)
	pipe.LTrim(ctx, BuyResultKey, 0, BuyResultCap-1)
	_, err = pipe.Exec(ctx)
	return err
}

// GetBuyResults returns the most recent n completed results.
func (c *Client) GetBuyResults(ctx context.Context, n int64) ([]model.BuyResult, error) {
	items, err := c.rdb.LRange(ctx, BuyResultKey, 0, n-1).Result()
	if err != nil {
		return nil, fmt.Errorf("cache: lrange buy results: %w", err)
	}
	results := make([]model.BuyResult, 0, len(items))
	for _, item := range items {
		var r model.BuyResult
		if err := json.Unmarshal([]byte(item), &r); err == nil {
			results = append(results, r)
		}
	}
	return results, nil
}

// ─── Generic read helpers ─────────────────────────────────────────────────────

// Get returns the raw string value for key, or ("", nil) on miss.
func (c *Client) Get(ctx context.Context, key string) (string, error) {
	val, err := c.rdb.Get(ctx, key).Result()
	if err == redis.Nil {
		return "", nil
	}
	return val, err
}

// TTL returns the remaining TTL of a key (-1 = no expiry, -2 = not found).
func (c *Client) TTL(ctx context.Context, key string) (time.Duration, error) {
	return c.rdb.TTL(ctx, key).Result()
}

// Keys returns all keys matching a glob pattern (e.g. "arb:*").
// ⚠️  Use only for debugging — KEYS blocks Redis on large datasets.
func (c *Client) Keys(ctx context.Context, pattern string) ([]string, error) {
	return c.rdb.Keys(ctx, pattern).Result()
}

// ScanKeys iterates keys matching pattern without blocking Redis.
// Returns all matching keys using SCAN internally.
func (c *Client) ScanKeys(ctx context.Context, pattern string) ([]string, error) {
	var keys []string
	iter := c.rdb.Scan(ctx, 0, pattern, 100).Iterator()
	for iter.Next(ctx) {
		keys = append(keys, iter.Val())
	}
	return keys, iter.Err()
}

// MGet returns values for multiple keys in one round-trip.
// Missing keys appear as "" in the result slice.
func (c *Client) MGet(ctx context.Context, keys ...string) ([]string, error) {
	vals, err := c.rdb.MGet(ctx, keys...).Result()
	if err != nil {
		return nil, fmt.Errorf("cache: mget: %w", err)
	}
	result := make([]string, len(vals))
	for i, v := range vals {
		if v != nil {
			result[i] = fmt.Sprintf("%v", v)
		}
	}
	return result, nil
}

// KeyType returns the Redis type of a key ("string", "list", "hash", etc.).
func (c *Client) KeyType(ctx context.Context, key string) (string, error) {
	return c.rdb.Type(ctx, key).Result()
}
