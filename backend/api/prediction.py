"""ML prediction endpoint — the flagship feature.

Exposes the full ship_value_analysis.py pipeline as a REST API:
GET /api/predict/{port_id}?lat=...&lon=...&duration=600
"""
from __future__ import annotations

import logging

from fastapi import APIRouter, HTTPException, Query

from models.schemas import PredictionOutput
from services.prediction import predict_port

logger = logging.getLogger(__name__)

router = APIRouter(prefix="/api/predict", tags=["prediction"])


@router.get("/{port_id}", response_model=PredictionOutput)
async def predict(
    port_id: str,
    port_name: str = Query("", description="Optional port name override"),
    lat: float | None = Query(None, description="Port latitude for AIS stream"),
    lon: float | None = Query(None, description="Port longitude for AIS stream"),
    duration: int = Query(600, alias="duration", description="AIS stream duration (seconds)"),
    use_live: bool = Query(True, alias="use_live", description="Stream live AIS traffic"),
):
    """Run the full prediction pipeline for a port.

    ═══════════════════════════════════════════════════════════
    This endpoint implements the **complete ship_value_analysis.py
    pipeline** in production:

    1. Resolves port industry profile from ArcGIS/DB
    2. Computes baseline macro forecast (model or heuristic)
    3. Optionally streams live AIS data for *duration* seconds
    4. Aggregates vessel types and applies traffic pressure factor
    5. Returns adjusted forecast with breakdown
    ═══════════════════════════════════════════════════════════
    """
    if not port_id:
        raise HTTPException(status_code=400, detail="port_id is required")

    result = await predict_port(
        port_id=port_id,
        port_name=port_name,
        lat=lat,
        lon=lon,
        duration_seconds=duration,
        use_live_traffic=use_live,
    )
    return PredictionOutput(**result)