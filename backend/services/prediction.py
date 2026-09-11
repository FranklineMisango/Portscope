"""ML prediction service - Live AIS vessel snapshot.
No fake forecasts. Only real data or nothing.
"""
from __future__ import annotations
import logging
from typing import Any
from config import settings
logger = logging.getLogger(__name__)

async def predict_port(port_id, port_name="", lat=None, lon=None, duration_seconds=300, use_live_traffic=True):
    """Take a live AIS snapshot and return vessel data. No fabricated forecasts."""
    from services.ais_stream import aggregate_vessels, stream_ais_snapshot
    from services.industry import get_port_industry_profile
    profile = await get_port_industry_profile(pageid=port_id, port_id=port_id, port_name=port_name)
    name = port_name or profile.get("portname", "") or port_id
    tops = [profile.get(f"industry_top{i}", "") for i in (1, 2, 3) if profile.get(f"industry_top{i}", "")]
    live_count = 0
    breakdown = {}
    stream_error = None
    if use_live_traffic and lat is not None and lon is not None:
        if not settings.AISSTREAM_API_KEY:
            stream_error = "No AIS API key configured"
        else:
            try:
                msgs = await stream_ais_snapshot(lat, lon, radius_km=15.0, duration_seconds=duration_seconds)
                agg = aggregate_vessels(msgs)
                live_count = agg["total_vessels"]
                for vb in agg.get("vessel_breakdown", []):
                    breakdown[vb["label"]] = vb["count"]
                if live_count == 0:
                    stream_error = "No vessels detected in range"
            except Exception as e:
                stream_error = f"AIS stream error: {e}"
    elif use_live_traffic:
        stream_error = "Port coordinates not available for streaming"
    return {
        "port_id": port_id, "port_name": name,
        "live_vessel_count": live_count,
        "vessel_breakdown": breakdown,
        "top_industries": tops,
        "industry_profile": profile,
        "stream_error": stream_error,
        # No fake forecasts - null when no model/data
        "baseline_forecast": None,
        "live_adjusted_forecast": None,
        "pressure_factor": 1.0 if live_count > 0 else None,
    }
