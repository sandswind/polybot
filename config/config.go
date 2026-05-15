// Package config loads runtime configuration from environment variables.
package config

import (
	"os"
	"strconv"
	"time"
)

// Config holds all runtime settings for the polybot scanner.
type Config struct {
	// Redis
	RedisAddr     string
	RedisPassword string
	RedisDB       int

	// Kalshi
	KalshiAPIKey string

	// Scanner behaviour
	ScanInterval time.Duration
	FetchLimit   int     // markets to fetch per platform per scan
	MinProfitPct float64

	// Category filter sent to each platform API
	Category string

	// Execution
	Bankroll      float64 // total capital in USD for Kelly sizing (0 = Kelly disabled)
	DryRun        bool    // true = log orders, never submit
	PythonBin     string  // path to python3 binary
	ExecutorDir   string  // directory containing order_executor.py

	// Lark (Feishu) notifications
	LarkWebhookURL string // Lark bot webhook URL (empty = disabled)
	LarkSecret     string // optional HMAC signing secret
}

// Load reads configuration from environment variables with sensible defaults.
func Load() Config {
	return Config{
		RedisAddr:     getEnv("REDIS_ADDR", "localhost:6379"),
		RedisPassword: getEnv("REDIS_PASSWORD", ""),
		RedisDB:       getEnvInt("REDIS_DB", 0),

		KalshiAPIKey: getEnv("KALSHI_API_KEY", ""),

		ScanInterval: getEnvDuration("SCAN_INTERVAL", 30*time.Second),
		FetchLimit:   getEnvInt("FETCH_LIMIT", 200),
		MinProfitPct: getEnvFloat("MIN_PROFIT_PCT", 0.02),

		Category: getEnv("MARKET_CATEGORY", "sports"),

		Bankroll:    getEnvFloat("BANKROLL_USD", 0),
		DryRun:      getEnvBool("DRY_RUN", true),
		PythonBin:   getEnv("PYTHON_BIN", "python3"),
		ExecutorDir: getEnv("EXECUTOR_DIR", "executor"),

		LarkWebhookURL: getEnv("LARK_WEBHOOK_URL", ""),
		LarkSecret:     getEnv("LARK_SECRET", ""),
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			return i
		}
	}
	return fallback
}

func getEnvFloat(key string, fallback float64) float64 {
	if v := os.Getenv(key); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return fallback
}

func getEnvBool(key string, fallback bool) bool {
	if v := os.Getenv(key); v != "" {
		switch v {
		case "true", "1", "yes":
			return true
		case "false", "0", "no":
			return false
		}
	}
	return fallback
}

func getEnvDuration(key string, fallback time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return fallback
}
