#!/usr/bin/env python3
"""
polybot order executor — thin wrapper around py-clob-client.

Called by the Go executor via subprocess:
    python3 order_executor.py '<json_payload>'

Input JSON schema:
{
  "market_id":   "0xabc...",   // Polymarket condition ID / token ID
  "side":        "BUY",        // BUY | SELL
  "outcome":     "YES",        // YES | NO
  "price":       0.58,         // limit price [0,1]
  "size":        50.0,         // number of contracts (USDC shares)
  "dry_run":     false         // if true, validate only — do NOT submit
}

Output JSON (always to stdout):
{
  "ok":       true | false,
  "order_id": "...",           // present on success
  "error":    "...",           // present on failure
  "dry_run":  false
}
"""

import json
import os
import sys
from typing import Any

# ── py-clob-client imports ────────────────────────────────────────────────────
try:
    from py_clob_client.client import ClobClient
    from py_clob_client.clob_types import OrderArgs, OrderType
    from py_clob_client.constants import POLYGON
except ImportError:
    _out({"ok": False, "error": "py-clob-client not installed. Run: pip install py-clob-client"})
    sys.exit(1)


def _out(data: dict[str, Any]) -> None:
    """Write JSON result to stdout and flush."""
    print(json.dumps(data), flush=True)


def build_client() -> ClobClient:
    """Initialise the CLOB client from environment variables."""
    key        = os.environ.get("POLY_PRIVATE_KEY", "")
    api_key    = os.environ.get("POLY_API_KEY", "")
    api_secret = os.environ.get("POLY_API_SECRET", "")
    api_passphrase = os.environ.get("POLY_API_PASSPHRASE", "")
    host       = os.environ.get("POLY_CLOB_HOST", "https://clob.polymarket.com")

    if not key:
        raise ValueError("POLY_PRIVATE_KEY env var is required for order placement")

    client = ClobClient(
        host,
        key=key,
        chain_id=POLYGON,
        creds={
            "apiKey":       api_key,
            "secret":       api_secret,
            "passphrase":   api_passphrase,
        },
    )
    return client


def run(payload: dict) -> dict:
    """Core execution logic."""
    market_id = payload.get("market_id", "")
    side      = payload.get("side", "BUY").upper()
    outcome   = payload.get("outcome", "YES").upper()
    price     = float(payload.get("price", 0))
    size      = float(payload.get("size", 0))
    dry_run   = bool(payload.get("dry_run", False))

    # ── Validation ────────────────────────────────────────────────────────────
    if not market_id:
        return {"ok": False, "error": "market_id is required"}
    if price <= 0 or price >= 1:
        return {"ok": False, "error": f"invalid price {price}: must be in (0, 1)"}
    if size <= 0:
        return {"ok": False, "error": f"invalid size {size}: must be > 0"}
    if side not in ("BUY", "SELL"):
        return {"ok": False, "error": f"invalid side: {side}"}

    if dry_run:
        return {
            "ok":      True,
            "dry_run": True,
            "message": f"DRY RUN — would place {side} {size} contracts of {outcome} "
                       f"on market {market_id} @ ${price:.4f}",
        }

    # ── Live order ────────────────────────────────────────────────────────────
    client = build_client()

    order_args = OrderArgs(
        price=price,
        size=size,
        side=side,
        token_id=market_id,
    )

    # create_order signs and submits the EIP-712 limit order
    resp = client.create_order(order_args)

    order_id = resp.get("orderID", "") if isinstance(resp, dict) else str(resp)
    return {
        "ok":       True,
        "dry_run":  False,
        "order_id": order_id,
        "raw":      resp if isinstance(resp, dict) else {},
    }


def main() -> None:
    if len(sys.argv) < 2:
        _out({"ok": False, "error": "usage: order_executor.py '<json>'"})
        sys.exit(1)

    try:
        payload = json.loads(sys.argv[1])
    except json.JSONDecodeError as e:
        _out({"ok": False, "error": f"invalid JSON: {e}"})
        sys.exit(1)

    try:
        result = run(payload)
        _out(result)
    except Exception as e:
        _out({"ok": False, "error": str(e)})
        sys.exit(1)


if __name__ == "__main__":
    main()
