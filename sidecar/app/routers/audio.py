"""Audio / ASR capability. Placeholder only: reserved for WhisperX in V2."""

from __future__ import annotations

from fastapi import APIRouter

from ..contracts import TranscribeRequest, TranscribeResponse
from ..placeholders import not_implemented

router = APIRouter(prefix="/v1/audio", tags=["audio"])


@router.post("/transcribe", response_model=TranscribeResponse)
async def transcribe(request: TranscribeRequest) -> TranscribeResponse:
    """Transcribe audio into word-level timestamps.

    Not implemented in the MVP. The contract exists so the word-level timestamp
    layer of the core data model can be built against a stable shape.
    """
    raise not_implemented(
        "audio",
        f"transcription of {request.audio_path!r} is not implemented; "
        "the audio/ASR engine lands in a later intent",
    )
