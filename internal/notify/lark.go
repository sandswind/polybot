// Package notify provides Lark (Feishu) webhook notifications for polybot.
//
// Setup:
//  1. 在飞书群里添加「自定义机器人」
//  2. 复制 Webhook URL → 填入 LARK_WEBHOOK_URL 环境变量
//  3. 如果开启了「签名校验」，把密钥填入 LARK_SECRET
//
// Webhook 文档：https://open.feishu.cn/document/client-docs/bot-v3/add-custom-bot
package notify

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/sandswind/polybot/internal/model"
)

// LarkClient sends rich-card messages to a Lark/Feishu group bot webhook.
type LarkClient struct {
	webhookURL string
	secret     string // optional signature secret
	http       *http.Client
}

// NewLarkClient creates a client. Pass empty secret if signature is disabled.
func NewLarkClient(webhookURL, secret string) *LarkClient {
	return &LarkClient{
		webhookURL: webhookURL,
		secret:     secret,
		http:       &http.Client{Timeout: 8 * time.Second},
	}
}

// IsConfigured returns true when a webhook URL has been provided.
func (c *LarkClient) IsConfigured() bool {
	return c.webhookURL != ""
}

// ── Message types ─────────────────────────────────────────────────────────────

type larkPayload struct {
	Timestamp string      `json:"timestamp,omitempty"`
	Sign      string      `json:"sign,omitempty"`
	MsgType   string      `json:"msg_type"`
	Card      interface{} `json:"card,omitempty"`
	Content   interface{} `json:"content,omitempty"`
}

// ── Public send helpers ───────────────────────────────────────────────────────

// SendOpportunity pushes a rich interactive card for an arb opportunity.
func (c *LarkClient) SendOpportunity(ctx context.Context, opp model.ArbitrageOpportunity) error {
	card := c.buildArbCard(opp)
	return c.send(ctx, larkPayload{MsgType: "interactive", Card: card})
}

// SendOrderResult pushes a plain-text order confirmation or error.
func (c *LarkClient) SendOrderResult(ctx context.Context, opp model.ArbitrageOpportunity, orderID string, execErr error) error {
	var text string
	if execErr != nil {
		text = fmt.Sprintf("❌ 下单失败\n市场: %s\n错误: %v", truncate(opp.PolyMarket.Question, 60), execErr)
	} else {
		text = fmt.Sprintf("✅ 下单成功\n市场: %s\nOrder ID: %s\n仓位: %.0f 合约 ($%.2f)\n预期利润: $%.2f",
			truncate(opp.PolyMarket.Question, 60),
			orderID,
			opp.KellyContracts,
			opp.KellyBetUSD,
			opp.ExpectedProfitUSD,
		)
	}
	return c.send(ctx, larkPayload{
		MsgType: "text",
		Content: map[string]string{"text": text},
	})
}

// SendStats pushes a periodic summary (call from scan loop if desired).
func (c *LarkClient) SendStats(ctx context.Context, scans, arbFound int64) error {
	text := fmt.Sprintf("📊 polybot 统计\n累计扫描: %d 次\n发现套利: %d 次\n时间: %s",
		scans, arbFound, time.Now().Format("2006-01-02 15:04:05"))
	return c.send(ctx, larkPayload{
		MsgType: "text",
		Content: map[string]string{"text": text},
	})
}

// ── Card builder ─────────────────────────────────────────────────────────────

func (c *LarkClient) buildArbCard(opp model.ArbitrageOpportunity) map[string]interface{} {
	// Header colour: green for high profit, yellow for moderate
	color := "yellow"
	if opp.ProfitPct >= 0.05 {
		color = "green"
	}

	kellyLine := "— (bankroll 未配置)"
	if opp.KellyContracts > 0 {
		kellyLine = fmt.Sprintf("%.0f 合约  ($%.2f)  预期利润 $%.2f",
			opp.KellyContracts, opp.KellyBetUSD, opp.ExpectedProfitUSD)
	}

	execMode := ""
	if opp.KellyContracts > 0 {
		execMode = "🤖 已触发自动下单"
	}

	elements := []map[string]interface{}{
		mdField("**平台匹配度**", fmt.Sprintf("%.0f / 100", opp.MatchScore)),
		mdField("**Polymarket**", truncate(opp.PolyMarket.Question, 70)),
		mdField("**Kalshi**", truncate(opp.KalshiMarket.Question, 70)),
		divider(),
		mdField("**操作**",
			fmt.Sprintf("BUY %s @ $%.4f on **%s**\nSELL %s @ $%.4f on **%s**",
				opp.Side, opp.BuyPrice, opp.BuyPlatform,
				opp.Side, opp.SellPrice, opp.SellPlatform,
			),
		),
		mdField("**净利润/单位**", fmt.Sprintf("$%.4f  (毛利 $%.4f)", opp.NetProfit, opp.GrossProfit)),
		mdField("**Kelly 仓位**", kellyLine),
	}

	if execMode != "" {
		elements = append(elements, mdField("**执行状态**", execMode))
	}

	return map[string]interface{}{
		"header": map[string]interface{}{
			"template": color,
			"title": map[string]interface{}{
				"tag":     "plain_text",
				"content": fmt.Sprintf("🎯 套利机会  [%s]  利润 %.2f%%", opp.Side, opp.ProfitPct*100),
			},
		},
		"elements": elements,
	}
}

func mdField(label, value string) map[string]interface{} {
	return map[string]interface{}{
		"tag": "div",
		"text": map[string]interface{}{
			"tag":     "lark_md",
			"content": label + "\n" + value,
		},
	}
}

func divider() map[string]interface{} {
	return map[string]interface{}{"tag": "hr"}
}

// ── HTTP send + optional HMAC signature ──────────────────────────────────────

func (c *LarkClient) send(ctx context.Context, payload larkPayload) error {
	if !c.IsConfigured() {
		return nil
	}

	if c.secret != "" {
		ts, sign := c.sign()
		payload.Timestamp = ts
		payload.Sign = sign
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("lark: marshal payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.webhookURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("lark: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("lark: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("lark: unexpected status %d", resp.StatusCode)
	}

	// Lark returns {"code":0,"msg":"success"} on success
	var result struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err == nil && result.Code != 0 {
		return fmt.Errorf("lark: api error code=%d msg=%s", result.Code, result.Msg)
	}

	return nil
}

// sign generates the HMAC-SHA256 signature required when security validation
// is enabled on the Lark bot.
// Formula: base64(hmac-sha256(timestamp+"\n"+secret))
func (c *LarkClient) sign() (timestamp, signature string) {
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	h := hmac.New(sha256.New, []byte(ts+"\n"+c.secret))
	signature = base64.StdEncoding.EncodeToString(h.Sum(nil))
	return ts, signature
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
