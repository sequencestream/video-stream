"""Shared helper for capabilities that are declared but not implemented."""

from __future__ import annotations

from fastapi import HTTPException, status

from .contracts import NotImplementedDetail


def not_implemented(capability: str, message: str) -> HTTPException:
    """Build the 501 raised by every placeholder endpoint."""
    detail = NotImplementedDetail(capability=capability, message=message)
    return HTTPException(status_code=status.HTTP_501_NOT_IMPLEMENTED, detail=detail.model_dump())
