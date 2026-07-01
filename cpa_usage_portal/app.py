"""Starlette app for the self-service CPA usage portal."""
from __future__ import annotations

import asyncio
import contextlib
import json
from pathlib import Path
from typing import Any

import httpx
from starlette.applications import Starlette
from starlette.requests import Request
from starlette.responses import HTMLResponse, JSONResponse, Response, StreamingResponse
from starlette.routing import Route

from .config import PortalConfig
from .cpamp import CPAMPClient, range_window
from .key_policy import KeyPolicyState
from .pricing import apply_event_pricing, apply_key_policy_pricing
from .redaction import safe_api_key_stats, safe_events
from .security import sha256_hex, sign_session, verify_session

_STATIC = Path(__file__).with_name("static")
_DASHBOARD = _STATIC / "dashboard.html"


class AuthError(Exception):
    pass


def _json_error(message: str, status_code: int) -> JSONResponse:
    return JSONResponse({"error": message}, status_code=status_code)


def _load_key_state(request: Request) -> KeyPolicyState:
    cfg: PortalConfig = request.app.state.cfg
    return KeyPolicyState.load(cfg.key_policy_state_path)


def _session_payload(request: Request) -> dict[str, Any]:
    cfg: PortalConfig = request.app.state.cfg
    token = request.cookies.get(cfg.session_cookie_name, "")
    payload = verify_session(token, cfg.session_secret)
    if payload is None:
        raise AuthError("not_authenticated")
    return payload


def _current_key(request: Request):
    payload = _session_payload(request)
    state = _load_key_state(request)
    record = state.get_by_raw_hash(str(payload.get("key_hash") or ""))
    if record is None or not record.enabled:
        raise AuthError("key_not_available")
    return record


def _parse_before(request: Request) -> tuple[int | None, int | None]:
    before_ms = request.query_params.get("before_ms")
    before_id = request.query_params.get("before_id")
    if request.query_params.get("before") and (not before_ms and not before_id):
        parts = request.query_params["before"].split(":", 1)
        before_ms = parts[0] if parts else None
        before_id = parts[1] if len(parts) > 1 else None
    try:
        parsed_ms = int(before_ms) if before_ms else None
    except ValueError:
        parsed_ms = None
    try:
        parsed_id = int(before_id) if before_id else None
    except ValueError:
        parsed_id = None
    return parsed_ms, parsed_id


async def dashboard(_request: Request) -> HTMLResponse:
    return HTMLResponse(_DASHBOARD.read_text(encoding="utf-8"))


async def healthz(request: Request) -> JSONResponse:
    cfg: PortalConfig = request.app.state.cfg
    state_ok = Path(cfg.key_policy_state_path).exists()
    cpamp_ok: bool | None = None
    try:
        await request.app.state.cpamp.health()
        cpamp_ok = True
    except Exception:
        cpamp_ok = False
    return JSONResponse({"ok": state_ok and bool(cpamp_ok), "key_policy_state": state_ok, "cpamp": cpamp_ok})


async def create_session(request: Request) -> JSONResponse:
    try:
        body = await request.json()
    except json.JSONDecodeError:
        return _json_error("invalid_json", 400)
    api_key = str((body or {}).get("api_key") or "").strip()
    if not api_key:
        return _json_error("api_key_required", 400)

    key_hash = sha256_hex(api_key)
    state = _load_key_state(request)
    record = state.get_by_raw_hash(key_hash)
    if record is None:
        return _json_error("invalid_api_key", 401)
    if not record.enabled:
        return _json_error("api_key_disabled", 403)

    cfg: PortalConfig = request.app.state.cfg
    token = sign_session(
        {"key_hash": record.raw_key_hash, "cpamp_hash": record.cpamp_hash, "key_name": record.name},
        cfg.session_secret,
        ttl_seconds=cfg.session_ttl_seconds,
    )
    response = JSONResponse({"me": record.safe_dict()})
    response.set_cookie(
        cfg.session_cookie_name,
        token,
        max_age=cfg.session_ttl_seconds,
        httponly=True,
        secure=cfg.cookie_secure,
        samesite="lax",
        path="/",
    )
    return response


async def delete_session(request: Request) -> JSONResponse:
    cfg: PortalConfig = request.app.state.cfg
    response = JSONResponse({"ok": True})
    response.delete_cookie(cfg.session_cookie_name, path="/")
    return response


async def me(request: Request) -> JSONResponse:
    try:
        record = _current_key(request)
    except AuthError as exc:
        return _json_error(str(exc), 401)
    return JSONResponse({"me": record.safe_dict()})


async def usage(request: Request) -> JSONResponse:
    try:
        record = _current_key(request)
    except AuthError as exc:
        return _json_error(str(exc), 401)
    window = range_window(request.query_params.get("range", "24h"))
    data = await request.app.state.cpamp.analytics(
        api_key_hash=record.cpamp_hash,
        window=window,
        include_events=False,
        include_model_stats=True,
    )
    data = apply_key_policy_pricing(data, record.model_prices)
    return JSONResponse({
        "range": request.query_params.get("range", "24h") if request.query_params.get("range") in {"24h", "7d"} else "24h",
        "from_ms": window.from_ms,
        "to_ms": window.to_ms,
        "summary": data.get("summary") or {},
        "timeline": data.get("timeline") or [],
        "model_share": data.get("model_share") or [],
        "model_stats": data.get("model_stats") or [],
        "api_key_stats": safe_api_key_stats(data.get("api_key_stats") or [], expected_hash=record.cpamp_hash),
    })


async def events(request: Request) -> JSONResponse:
    try:
        record = _current_key(request)
    except AuthError as exc:
        return _json_error(str(exc), 401)
    limit_raw = request.query_params.get("limit", "100")
    try:
        limit = max(1, min(int(limit_raw), 200))
    except ValueError:
        limit = 100
    before_ms, before_id = _parse_before(request)
    window = range_window("7d")
    data = await request.app.state.cpamp.analytics(
        api_key_hash=record.cpamp_hash,
        window=window,
        include_events=True,
        event_limit=limit,
        before_ms=before_ms,
        before_id=before_id,
    )
    page = data.get("events") or {}
    items = apply_event_pricing(
        safe_events(page.get("items") or [], expected_hash=record.cpamp_hash),
        record.model_prices,
    )
    return JSONResponse({
        "events": items,
        "next_before_ms": page.get("next_before_ms") or 0,
        "next_before_id": page.get("next_before_id") or 0,
        "has_more": bool(page.get("has_more")),
        "total_count": page.get("total_count") or len(items),
    })


def _sse(name: str, payload: Any) -> bytes:
    data = json.dumps(payload, ensure_ascii=False, separators=(",", ":"))
    return f"event: {name}\ndata: {data}\n\n".encode("utf-8")


async def events_stream(request: Request) -> StreamingResponse:
    try:
        record = _current_key(request)
    except AuthError as exc:
        return StreamingResponse(iter([_sse("error", {"error": str(exc)})]), media_type="text/event-stream")

    cfg: PortalConfig = request.app.state.cfg

    async def stream():
        seen: set[str] = set()
        yield _sse("ready", {"ok": True})
        while True:
            if await request.is_disconnected():
                break
            try:
                data = await request.app.state.cpamp.analytics(
                    api_key_hash=record.cpamp_hash,
                    window=range_window("24h"),
                    include_events=True,
                    event_limit=20,
                )
                page = data.get("events") or {}
                items = apply_event_pricing(
                    safe_events(page.get("items") or [], expected_hash=record.cpamp_hash),
                    record.model_prices,
                )
                for item in reversed(items):
                    event_hash = str(item.get("event_hash") or "")
                    if event_hash and event_hash not in seen:
                        seen.add(event_hash)
                        yield _sse("event", item)
            except Exception as exc:
                yield _sse("error", {"error": type(exc).__name__})
            await asyncio.sleep(max(1.0, cfg.poll_seconds))

    return StreamingResponse(
        stream(),
        media_type="text/event-stream",
        headers={"Cache-Control": "no-cache", "X-Accel-Buffering": "no"},
    )


def create_app(cfg: PortalConfig) -> Starlette:
    if not cfg.session_secret:
        raise ValueError("CPA_USAGE_PORTAL_SESSION_SECRET or file is required")
    if not cfg.cpamp_admin_key:
        raise ValueError("CPA_USAGE_PORTAL_CPAMP_ADMIN_KEY or file is required")

    @contextlib.asynccontextmanager
    async def lifespan(app: Starlette):
        client = httpx.AsyncClient()
        app.state.cfg = cfg
        app.state.client = client
        app.state.cpamp = CPAMPClient(cfg.cpamp_base_url, cfg.cpamp_admin_key, client)
        try:
            yield
        finally:
            await client.aclose()

    routes = [
        Route("/", dashboard, methods=["GET"]),
        Route("/healthz", healthz, methods=["GET"]),
        Route("/api/session", create_session, methods=["POST"]),
        Route("/api/session", delete_session, methods=["DELETE"]),
        Route("/api/me", me, methods=["GET"]),
        Route("/api/usage", usage, methods=["GET"]),
        Route("/api/events", events, methods=["GET"]),
        Route("/api/events/stream", events_stream, methods=["GET"]),
    ]
    return Starlette(routes=routes, lifespan=lifespan)
