# ── Stage 1: Go build ────────────────────────────────────────────────────────
FROM golang:1.22-alpine AS go-builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /polybot ./cmd/scanner

# ── Stage 2: Python deps ──────────────────────────────────────────────────────
FROM python:3.12-slim AS py-builder

WORKDIR /app
COPY executor/requirements.txt ./
RUN pip install --no-cache-dir --target=/pylibs -r requirements.txt

# ── Stage 3: Runtime ──────────────────────────────────────────────────────────
FROM python:3.12-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates tzdata \
 && rm -rf /var/lib/apt/lists/*

# Go binary
COPY --from=go-builder /polybot /polybot

# Python executor script + installed libs
COPY --from=py-builder /pylibs /usr/local/lib/python3.12/site-packages
COPY executor/ /app/executor/

WORKDIR /app
ENTRYPOINT ["/polybot"]
