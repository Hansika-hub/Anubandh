"""The web container serves the SPA and proxies the backends under one origin."""

from __future__ import annotations

import httpx
import pytest

from conftest import WEB


@pytest.fixture(scope="module")
def web() -> httpx.Client:
    try:
        httpx.get(WEB, timeout=3).raise_for_status()
    except httpx.HTTPError as e:
        pytest.skip(f"web not running at {WEB} ({e})")
    return httpx.Client(base_url=WEB, timeout=20)


def test_spa_and_deep_links(web: httpx.Client) -> None:
    for path in ("/", "/deals/DL-20261020-0001", "/treasury"):
        r = web.get(path)
        assert r.status_code == 200 and "<div id=\"root\">" in r.text, path


def test_backends_are_proxied(web: httpx.Client) -> None:
    assert web.get("/api/v1/health").json()["status"] == "ok"
    assert web.get("/forecast/v1/health").json()["status"] == "ok"
    reg = web.get("/udyam/v1/UDYAM-KL-00-0000007").json()
    assert (reg["category"], reg["supplierId"]) == ("SMALL", "SUP-007")


def test_internal_api_is_not_exposed(web: httpx.Client) -> None:
    r = web.post("/internal/v1/submit", json={})
    assert r.status_code != 200 or "application/json" not in r.headers.get("content-type", "")
    r = web.get("/internal/v1/mock/clock")
    assert "application/json" not in r.headers.get("content-type", ""), "gateway /internal must not be reachable through the web"


def test_sse_stream_through_proxy(web: httpx.Client) -> None:
    with web.stream("GET", "/api/v1/events/stream?org=BUYER", timeout=10) as r:
        assert r.status_code == 200 and r.headers["content-type"].startswith("text/event-stream")
        first = next(r.iter_lines())
        assert first.startswith(("retry:", ":", "id:", "event:"))
