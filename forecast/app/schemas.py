from __future__ import annotations

from pydantic import BaseModel, Field


class WhatIfRequest(BaseModel):
    orgId: str = Field(min_length=1)
    shiftDealIds: list[str] = Field(default_factory=list)
    days: int = Field(ge=0, le=365)
