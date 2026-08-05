"""Request/response contracts shared by the sidecar routers.

These models are the Python half of the sidecar contract; the Go half lives in
``internal/sidecar/contract.go``. Keeping both sides declarative is what lets a
later intent swap in a real implementation without renegotiating the boundary.
"""

from __future__ import annotations

from typing import Any

from pydantic import BaseModel, Field

# Capability identifiers reserved by the MVP. Each has a declared contract and a
# placeholder implementation; none has real logic yet.
CAPABILITIES: tuple[str, ...] = ("audio", "jianying", "browser")


class Health(BaseModel):
    """The sidecar's self-report."""

    status: str
    service: str
    version: str
    capabilities: list[str]


class UpstreamHealth(BaseModel):
    """Result of probing the Go main service."""

    status: str
    upstream_url: str
    reachable: bool
    upstream_version: str | None = None
    error: str | None = None


class NotImplementedDetail(BaseModel):
    """Structured body of a 501 response.

    A placeholder must be machine-distinguishable from a real failure, so every
    unimplemented capability answers with this envelope rather than an opaque
    error string or, worse, fabricated data.
    """

    code: str = "not_implemented"
    message: str
    capability: str


class TranscribeRequest(BaseModel):
    """Transcribe an audio file into word-level timestamps."""

    audio_path: str = Field(description="Absolute path to the audio file to transcribe")
    language: str | None = Field(default=None, description="BCP-47 hint, or None to auto-detect")


class Word(BaseModel):
    """One recognised token with its timing in seconds."""

    text: str
    start: float
    end: float


class TranscribeResponse(BaseModel):
    """Transcription output once the capability is implemented."""

    text: str
    words: list[Word]


class DraftRequest(BaseModel):
    """Emit an editor draft project for a rendered video project."""

    project_id: str
    output_dir: str


class DraftResponse(BaseModel):
    """Location of the generated draft."""

    draft_path: str


class AutomateRequest(BaseModel):
    """Drive a browser session against a target platform."""

    target: str
    action: str
    payload: dict[str, Any] | None = None


class AutomateResponse(BaseModel):
    """Outcome of a browser automation run."""

    succeeded: bool
    data: dict[str, Any] | None = None
