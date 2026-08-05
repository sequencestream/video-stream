"""Browser automation capability. Placeholder only."""

from __future__ import annotations

from fastapi import APIRouter

from ..contracts import AutomateRequest, AutomateResponse
from ..placeholders import not_implemented

router = APIRouter(prefix="/v1/browser", tags=["browser"])


@router.post("/automate", response_model=AutomateResponse)
async def automate(request: AutomateRequest) -> AutomateResponse:
    """Drive a browser session against a target platform.

    Not implemented in the MVP.
    """
    raise not_implemented(
        "browser",
        f"action {request.action!r} on target {request.target!r} is not implemented; "
        "browser automation lands in a later intent",
    )
