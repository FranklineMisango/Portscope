"""ArcGIS FeatureServer client — both PortWatch databases.

This is the **critical bridge** between the ArcGIS data layer and the
PortWatch / IMF analytics.  It mirrors the Python reference's
``query_port_database()`` function (ship_value_analysis.py line 350–363)
and extracts the industry metadata that the Go app was silently dropping.
"""
from __future__ import annotations

import json
import logging
from typing import Any

import httpx

from config import settings

logger = logging.getLogger(__name__)

# ── FeatureServer endpoints (match Go ingest/arcgis_sync.go) ──

PORTS_URL: str = settings.ARCGIS_PORTS_URL
CHOKEPOINTS_URL: str = settings.ARCGIS_CHOKEPOINTS_URL


async def _fetch_arcgis(
    url: str,
    where: str = "1=1",
    out_fields: str = "*",
    return_geometry: str = "false",
    out_sr: int = 4326,
    max_records: int = 2000,
) -> list[dict[str, Any]]:
    """Query an ArcGIS FeatureServer REST endpoint with pagination."""
    client = httpx.AsyncClient(timeout=30.0)
    all_features: list[dict[str, Any]] = []
    offset = 0

    try:
        while offset < max_records:
            params: dict[str, Any] = {
                "where": where,
                "outFields": out_fields,
                "returnGeometry": return_geometry,
                "outSR": out_sr,
                "f": "json",
                "resultRecordCount": 1000,
                "resultOffset": offset,
            }
            resp = await client.get(url, params=params)
            resp.raise_for_status()
            data = resp.json()

            features = data.get("features", [])
            for feat in features:
                attrs = feat.get("attributes", {})
                if attrs:
                    all_features.append(attrs)

            if not data.get("exceededTransferLimit") or len(features) < 1000:
                break
            offset += 1000
    except httpx.HTTPError as exc:
        logger.warning("ArcGIS fetch error for %s: %s", url, exc)
    finally:
        await client.aclose()

    return all_features


# ── Port Industry Profile ──────────────────────────────────

def parse_industry_profile(attrs: dict[str, Any]) -> dict[str, Any]:
    """Extract the industry metadata that the Go app was ignoring.

    Mirrors ship_value_analysis.py line 351–363 (``query_port_database``).
    Returns a dict with keys: portid, portname, country, industry_top1/2/3.
    """
    return {
        "portid": attrs.get("portid", ""),
        "portname": attrs.get("portname", ""),
        "country": attrs.get("country", ""),
        "industry_top1": attrs.get("industry_top1", ""),
        "industry_top2": attrs.get("industry_top2", ""),
        "industry_top3": attrs.get("industry_top3", ""),
    }


async def get_port_industry(port_id: str) -> dict[str, Any] | None:
    """Query the ArcGIS PortWatch ports database for a port's industry profile.

    This is the **direct equivalent** of the Python reference's
    ``query_port_database(port_id)`` call at line 350–363.
    """
    # Try with the portid as-is, also try uppercase/lowercase
    candidates = [port_id, port_id.lower(), port_id.upper()]
    for pid in candidates:
        where = f"portid = '{pid}'"
        features = await _fetch_arcgis(
            PORTS_URL,
            where=where,
            out_fields="portid,portname,country,industry_top1,industry_top2,"
                       "industry_top3",
        )
        if features:
            return parse_industry_profile(features[0])

    # Fallback: search by pageid
    where = f"pageid = '{port_id}'"
    features = await _fetch_arcgis(
        PORTS_URL,
        where=where,
        out_fields="portid,portname,country,industry_top1,industry_top2,"
                   "industry_top3",
    )
    if features:
        return parse_industry_profile(features[0])

    return None


async def get_chokepoint_data(pageid: str) -> dict[str, Any] | None:
    """Fetch chokepoint metadata from ArcGIS."""
    where = f"pageid = '{pageid}'"
    features = await _fetch_arcgis(
        CHOKEPOINTS_URL,
        where=where,
        out_fields="*",
    )
    if features:
        return features[0]
    return None


# ── Bulk historical data (for ML training) ─────────────────

async def fetch_port_history(
    port_id: str,
    limit: int = 500,
) -> list[dict[str, Any]]:
    """Fetch historical portcall / import / export records for ML training.

    Returns rows with fields: date, portcalls, import, export, n_total, etc.
    Mirrors what ship_value_analysis.py loads from CSVs.
    """
    where = f"portid = '{port_id}'"
    features = await _fetch_arcgis(
        PORTS_URL,
        where=where,
        out_fields="date,portcalls,import,export,n_total,portid,portname",
        return_geometry="false",
        max_records=limit,
    )
    # ArcGIS returns sorted by date DESC by default; reverse for chronological
    features.reverse()
    return features


async def search_ports_arcgis(query: str) -> list[dict[str, Any]]:
    """Search ports in ArcGIS by name (LIKE)."""
    where = f"UPPER(portname) LIKE UPPER('%{query}%')"
    features = await _fetch_arcgis(
        PORTS_URL,
        where=where,
        out_fields="portid,portname,country,pageid,industry_top1,lat,lon",
        return_geometry="true",
    )
    return features