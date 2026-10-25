"""Shared helpers for the Anubandh simulators (BUILD.md section 11).

Every simulator returns errors in the shared shape {"error": {"code", "message", "details"}}
(BUILD.md section 12), exposes GET /health, and keeps its state in memory per app instance,
so tests get a fresh simulator from each create_app() call.
"""

from __future__ import annotations

import hashlib
import hmac
import json
import os
from functools import cache
from pathlib import Path
from typing import Any

import httpx
from fastapi import FastAPI, Request
from fastapi.exceptions import RequestValidationError
from fastapi.responses import JSONResponse

FIXTURES = Path(__file__).resolve().parent / "fixtures"


class SimError(Exception):
    """An error returned to the caller in the shared error shape."""

    def __init__(self, status: int, code: str, message: str, details: dict[str, Any] | None = None):
        super().__init__(message)
        self.status, self.code, self.message, self.details = status, code, message, details or {}


def error_body(code: str, message: str, details: dict[str, Any] | None = None) -> dict[str, Any]:
    return {"error": {"code": code, "message": message, "details": details or {}}}


def new_app(svc: str, title: str) -> FastAPI:
    app = FastAPI(title=title, version="1.0.0")

    @app.exception_handler(SimError)
    async def _sim_error(_: Request, e: SimError) -> JSONResponse:
        return JSONResponse(error_body(e.code, e.message, e.details), status_code=e.status)

    @app.exception_handler(RequestValidationError)
    async def _bad_request(_: Request, e: RequestValidationError) -> JSONResponse:
        problems = [{"field": ".".join(str(p) for p in err["loc"][1:]), "problem": err["msg"]} for err in e.errors()]
        return JSONResponse(error_body("ERR_BAD_REQUEST", "invalid request body", {"problems": problems}), status_code=400)

    @app.get("/health")
    def health() -> dict[str, str]:
        return {"status": "ok", "svc": svc}

    return app


@cache
def fixture(name: str) -> Any:
    return json.loads((FIXTURES / name).read_text(encoding="utf-8"))


def suppliers() -> dict[str, dict[str, Any]]:
    return {s["supplierId"]: s for s in fixture("parties.json")["suppliers"]}


# ---- signed webhooks to ingest (BUILD.md section 9.2) ----


def ingest_url() -> str:
    return os.getenv("INGEST_URL", "http://ingest-api:8081").rstrip("/")


def sign(body: bytes, source: str) -> str:
    """hex(HMAC-SHA256(body, HMAC_SECRET_<SOURCE>)), as ingest verifies it."""
    secret = os.getenv("HMAC_SECRET_" + source.upper(), "")
    if not secret:
        raise SimError(500, "ERR_INTERNAL", f"HMAC_SECRET_{source.upper()} is not set")
    return hmac.new(secret.encode(), body, hashlib.sha256).hexdigest()


async def send_webhook(client: httpx.AsyncClient, path: str, source: str, envelope: dict[str, Any]) -> dict[str, Any]:
    """POSTs one signed InboundEvent to /ingest/v1/events/<path>; returns {status, body}."""
    body = json.dumps(envelope, separators=(",", ":")).encode()
    headers = {
        "Content-Type": "application/json",
        "X-Source": source,
        "X-Signature": sign(body, source),
        "X-Idempotency-Key": envelope["idemKey"],
    }
    try:
        r = await client.post(f"{ingest_url()}/ingest/v1/events/{path}", content=body, headers=headers)
    except httpx.HTTPError as e:
        return {"status": 0, "body": {"error": str(e)}}
    try:
        parsed: Any = r.json()
    except ValueError:
        parsed = r.text
    return {"status": r.status_code, "body": parsed}
