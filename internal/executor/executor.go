// Package executor calls the Python order_executor.py script via subprocess
// to place orders on Polymarket through py-clob-client.
//
// Architecture decision: the Go scanner owns the strategy loop; Python owns
// the Polymarket signing/submission because py-clob-client (the official SDK)
// is Python-only.  The two processes communicate through a single JSON arg and
// a single JSON line on stdout.
package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// OrderRequest is the payload sent to the Python executor.
type OrderRequest struct {
	MarketID string  `json:"market_id"` // Polymarket token/condition ID
	Side     string  `json:"side"`      // "BUY" | "SELL"
	Outcome  string  `json:"outcome"`   // "YES" | "NO"
	Price    float64 `json:"price"`     // limit price in [0,1]
	Size     float64 `json:"size"`      // number of contracts (USDC shares)
	DryRun   bool    `json:"dry_run"`   // true → validate only, no submission
}

// OrderResult is the JSON response parsed from the Python executor's stdout.
type OrderResult struct {
	OK      bool   `json:"ok"`
	OrderID string `json:"order_id"`
	Error   string `json:"error"`
	DryRun  bool   `json:"dry_run"`
	Message string `json:"message"` // dry-run description
}

// Executor manages subprocess calls to order_executor.py.
type Executor struct {
	pythonBin  string // path to python3 binary
	scriptPath string // path to order_executor.py
	timeout    time.Duration
	dryRun     bool
	env        []string // extra env vars (POLY_PRIVATE_KEY etc.)
}

// Config holds Executor configuration.
type Config struct {
	// PythonBin is the python3 executable (default: "python3").
	PythonBin string
	// ScriptDir is the directory that contains order_executor.py.
	// Defaults to ./executor relative to the working directory.
	ScriptDir string
	// Timeout for a single order call (default: 15s).
	Timeout time.Duration
	// DryRun: when true every order is validated but never submitted.
	DryRun bool
}

// DefaultConfig returns safe defaults.
func DefaultConfig() Config {
	return Config{
		PythonBin: "python3",
		ScriptDir: "executor",
		Timeout:   15 * time.Second,
		DryRun:    true, // safe default — must opt-in to live trading
	}
}

// New creates an Executor.  It resolves the script path and propagates the
// current process environment so POLY_PRIVATE_KEY etc. are available.
func New(cfg Config) (*Executor, error) {
	if cfg.PythonBin == "" {
		cfg.PythonBin = "python3"
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 15 * time.Second
	}

	script := filepath.Join(cfg.ScriptDir, "order_executor.py")
	if _, err := os.Stat(script); err != nil {
		return nil, fmt.Errorf("executor: script not found at %s: %w", script, err)
	}

	return &Executor{
		pythonBin:  cfg.PythonBin,
		scriptPath: script,
		timeout:    cfg.Timeout,
		dryRun:     cfg.DryRun,
		env:        os.Environ(),
	}, nil
}

// Execute places a single order.  DryRun is OR-ed with the executor-level flag
// so callers can force dry-run per-order.
func (e *Executor) Execute(ctx context.Context, req OrderRequest) (OrderResult, error) {
	req.DryRun = req.DryRun || e.dryRun

	payload, err := json.Marshal(req)
	if err != nil {
		return OrderResult{}, fmt.Errorf("executor: marshal request: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, e.timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, e.pythonBin, e.scriptPath, string(payload))
	cmd.Env = e.env

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		errMsg := stderr.String()
		if errMsg == "" {
			errMsg = err.Error()
		}
		return OrderResult{OK: false, Error: errMsg},
			fmt.Errorf("executor: python script failed: %w — stderr: %s", err, errMsg)
	}

	var result OrderResult
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &result); err != nil {
		return OrderResult{}, fmt.Errorf("executor: parse output %q: %w", stdout.String(), err)
	}
	return result, nil
}

// IsAvailable returns true if python3 and the script are accessible.
// Call this at startup and skip order execution gracefully if it returns false.
func (e *Executor) IsAvailable() bool {
	_, err := exec.LookPath(e.pythonBin)
	if err != nil {
		return false
	}
	_, err = os.Stat(e.scriptPath)
	return err == nil
}
