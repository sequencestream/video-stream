"""video-stream Python sidecar.

The sidecar isolates the Python ecosystem (audio/ASR, editor drafts, browser
automation) from the Go main service. In the MVP it carries no business logic:
it exists so that V2 can adopt WhisperX and friends without restructuring the
main service.
"""

from __future__ import annotations

import os

import httpx
from fastapi import APIRouter, FastAPI

from .contracts import CAPABILITIES, Health, UpstreamHealth
from .routers import audio, browser, jianying

VERSION = os.getenv("VS_SIDECAR_VERSION", "0.1.0-dev")
UPSTREAM_URL = os.getenv("VS_UPSTREAM_URL", "http://127.0.0.1:8080").rstrip("/")
UPSTREAM_TIMEOUT = float(os.getenv("VS_UPSTREAM_TIMEOUT_SECONDS", "5"))

health_router = APIRouter(tags=["health"])


@health_router.get("/health", response_model=Health)
async def health() -> Health:
    """Report on this process only.

    This endpoint must never depend on the main service: the two health checks
    probe each other, and making both transitive would deadlock them into
    reporting each other's outage.
    """
    return Health(status="ok", service="video-stream-sidecar", version=VERSION, capabilities=list(CAPABILITIES))


@health_router.get("/health/upstream", response_model=UpstreamHealth)
async def upstream_health() -> UpstreamHealth:
    """Probe the Go main service.

    This is the sidecar half of the mutual reachability check; the main service
    half is its ``/readyz``. An unreachable upstream is reported as ``degraded``
    with HTTP 200 so a transient outage does not get the sidecar restarted.
    """
    try:
        # trust_env=False: the upstream is a loopback address or a sibling
        # container, so routing the probe through the developer's HTTP(S)_PROXY
        # would report a false outage.
        async with httpx.AsyncClient(timeout=UPSTREAM_TIMEOUT, trust_env=False) as client:
            response = await client.get(f"{UPSTREAM_URL}/healthz")
            response.raise_for_status()
            body = response.json()
    except Exception as exc:  # noqa: BLE001 - any failure means "not reachable"
        return UpstreamHealth(
            status="degraded",
            upstream_url=UPSTREAM_URL,
            reachable=False,
            error=f"{type(exc).__name__}: {exc}",
        )

    return UpstreamHealth(
        status="ok",
        upstream_url=UPSTREAM_URL,
        reachable=True,
        upstream_version=body.get("version"),
    )


def create_app() -> FastAPI:
    """Build the sidecar application."""
    app = FastAPI(
        title="video-stream sidecar",
        version=VERSION,
        description="Python-side capabilities for video-stream. MVP exposes contracts only.",
    )
    app.include_router(health_router)
    app.include_router(audio.router)
    app.include_router(jianying.router)
    app.include_router(browser.router)
    return app


app = create_app()
