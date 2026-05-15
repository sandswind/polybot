# polybot

跨平台预测市场套利扫描器 MVP — **Polymarket × Kalshi**，Go + Redis。

## 工作原理

```
每隔 N 秒
  ├─ 并发拉取 Polymarket（Gamma API）和 Kalshi（REST API）市场数据
  ├─ 写入 Redis 缓存（TTL 60s，避免重复请求）
  ├─ 模糊匹配同一事件（Sørensen–Dice bigram 相似度）
  ├─ 计算双平台价差，扣除手续费后是否超过阈值
  └─ 新机会写入 Redis（5 分钟去重），打印终端报告
```

## 目录结构

```
polybot/
├── cmd/scanner/main.go          # 主入口，扫描循环
├── internal/
│   ├── model/market.go          # 数据结构
│   ├── fetcher/
│   │   ├── polymarket.go        # Polymarket Gamma API 客户端
│   │   └── kalshi.go            # Kalshi REST API 客户端
│   ├── matcher/matcher.go       # 模糊市场名称匹配
│   ├── cache/redis.go           # Redis 缓存 + 去重
│   └── engine/arbitrage.go      # 套利计算引擎
├── config/config.go             # 环境变量配置
├── docker-compose.yml           # Redis + scanner 一键启动
└── Dockerfile
```

## 快速开始

### 本地运行（需要本地 Redis）

```bash
# 1. 复制环境变量
cp .env.example .env

# 2. 启动 Redis
docker run -d -p 6379:6379 redis:7-alpine

# 3. 运行扫描器
go run ./cmd/scanner
```

### Docker Compose 一键启动

```bash
cp .env.example .env
docker compose up --build
```

## 环境变量

| 变量 | 默认值 | 说明 |
|------|--------|------|
| `REDIS_ADDR` | `localhost:6379` | Redis 地址 |
| `KALSHI_API_KEY` | _(空)_ | Kalshi API Key（只读扫描可不填）|
| `SCAN_INTERVAL` | `30s` | 扫描间隔 |
| `FETCH_LIMIT` | `200` | 每平台每次拉取的市场数 |
| `MIN_PROFIT_PCT` | `0.02` | 最低净利润率（2%）|
| `MARKET_CATEGORY` | `sports` | 市场分类：sports / politics / crypto |

## 套利逻辑

```
同一事件，两平台价格不一致时：

  Kalshi  YES = $0.58
  Polymarket YES = $0.64

  → 在 Kalshi 买入 YES @ $0.58
  → 事件发生时两平台都结算 $1.00
  → 毛利 = $0.06，扣费后净利 ≈ $0.045（~7.8%）
```

手续费估算：Polymarket taker ~1%，Kalshi taker ~0.5%。
