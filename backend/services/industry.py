"""Port industry profile resolution — bridging ArcGIS and PortWatch/IMF.

This service solves the core problem the Go app had: industry metadata
from ArcGIS was stored in ``source_payload`` JSONB but never surfaced
into the API response.
"""
from __future__ import annotations

import json
import logging
from typing import Any

import asyncpg

from db import pg_conn
from services.arcgis import get_port_industry

logger = logging.getLogger(__name__)


async def get_port_industry_profile(
    pageid: str,
    port_id: str = "",
    port_name: str = "",
) -> dict[str, Any]:
    """Resolve a port's industry profile.

    Strategy (tried in order):
    1. Check Postgres ``ports.source_payload`` for ``industry_top1/2/3``
       (the Go ingest already stored this, but never exposed it).
    2. Fallback: query ArcGIS FeatureServer live.
    3. Return empty profile if neither works.
    """
    # ── Try local DB first ───────────────────────────────
    profile = await _profile_from_db(pageid, port_id, port_name)
    if profile and profile.get("industry_top1"):
        return profile

    # ── Fallback to ArcGIS API ────────────────────────────
    lookup_id = port_id or pageid
    if lookup_id:
        arcgis_profile = await get_port_industry(lookup_id)
        if arcgis_profile:
            return arcgis_profile

    return {
        "portid": port_id or "",
        "portname": port_name or "",
        "industry_top1": "",
        "industry_top2": "",
        "industry_top3": "",
        "industry_code1": "",
        "industry_code2": "",
        "industry_code3": "",
    }


async def _profile_from_db(
    pageid: str,
    port_id: str,
    port_name: str,
) -> dict[str, Any] | None:
    """Extract industry profile from the local Postgres ``source_payload``."""
    try:
        async with pg_conn() as conn:
            # Try matching by pageid, portid, or name
            if pageid:
                row = await conn.fetchrow(
                    "SELECT source_payload FROM ports WHERE source_payload->>'pageid' = $1",
                    pageid,
                )
                if row:
                    return _extract_from_payload(row["source_payload"])

            if port_id:
                row = await conn.fetchrow(
                    "SELECT source_payload FROM ports WHERE source_payload->>'portid' = $1",
                    port_id,
                )
                if row:
                    return _extract_from_payload(row["source_payload"])

            if port_name:
                row = await conn.fetchrow(
                    "SELECT source_payload FROM ports WHERE name = $1",
                    port_name,
                )
                if row:
                    return _extract_from_payload(row["source_payload"])
    except Exception as exc:
        logger.warning("DB profile lookup error: %s", exc)

    return None


def _extract_from_payload(payload: Any) -> dict[str, Any]:
    """Extract industry fields from the JSONB source_payload.

    This is the **critical fix** — the Go app stored the data but never
    unpacked these fields into the API response.
    """
    if isinstance(payload, str):
        payload = json.loads(payload)
    elif isinstance(payload, asyncpg.Record):
        payload = dict(payload)
    if not isinstance(payload, dict):
        payload = {}

    return {
        "portid": payload.get("portid", ""),
        "portname": payload.get("portname", "") or payload.get("fullname", ""),
        "country": payload.get("country", ""),
        "pageid": payload.get("pageid", ""),
        "industry_top1": payload.get("industry_top1", ""),
        "industry_top2": payload.get("industry_top2", ""),
        "industry_top3": payload.get("industry_top3", ""),
        "industry_code1": payload.get("industry_code1", ""),
        "industry_code2": payload.get("industry_code2", ""),
        "industry_code3": payload.get("industry_code3", ""),
        "lat": payload.get("lat", 0.0),
        "lon": payload.get("lon", 0.0),
    }