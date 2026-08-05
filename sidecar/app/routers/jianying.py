"""JianYing (CapCut) draft capability. Placeholder only."""

from __future__ import annotations

from fastapi import APIRouter

from ..contracts import DraftRequest, DraftResponse
from ..placeholders import not_implemented

router = APIRouter(prefix="/v1/jianying", tags=["jianying"])


@router.post("/draft", response_model=DraftResponse)
async def create_draft(request: DraftRequest) -> DraftResponse:
    """Write an editable draft project for a video project.

    Not implemented in the MVP.
    """
    raise not_implemented(
        "jianying",
        f"draft export for project {request.project_id!r} is not implemented; "
        "the draft writer lands in a later intent",
    )
