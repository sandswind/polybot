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
