"""PortWatch / IMF data endpoints — the core ArcGIS ↔ PortWatch bridge.

Fixes the two critical failures in the Go API:
1. Industry metadata extraction — surfaces industry_top1/2/3 from ArcGIS
2. Time-series data — serves portcall/import/export history
"""
from __future__ import annotations

import json
import logging
from typing import Any

from fastapi import APIRouter, Query

from db import pg_conn
from models.schemas import (
    PortBrief,
    PortWatchMetrics,
    PortWatchPageResult,
    PortWatchTimeSeries,
    SearchResult,
    TimeSeriesPoint,
)
from services.arcgis import get_port_industry
from services.local_data import get_local_port, search_local

logger = logging.getLogger(__name__)

router = APIRouter(prefix="/api/portwatch", tags=["portwatch"])
# ── Metrics for a specific pageid ─────────────────────────


@router.get("/page/{pageid}", response_model=PortWatchPageResult)
async def get_portwatch_page(pageid: str):
    """Get full PortWatch data for a pageid.

    Fixes Go failure #1 and #5: resolves industry metadata from
    ``source_payload`` JSONB and ArcGIS fallback.
    """
    result = PortWatchPageResult(
        pageid=pageid,
        external_url=f"https://portwatch.imf.org/pages/{pageid}",
    )

    try:
        async with pg_conn() as conn:
            # Try ports table first
            row = await conn.fetchrow(
                """SELECT id, name, country, iso3,
                          ST_X(geom) AS lon, ST_Y(geom) AS lat,
                          source_payload
                   FROM ports
                   WHERE source_payload->>'pageid' = $1
                      OR source_payload->>'portid' = $1
                   LIMIT 1""",
                pageid,
            )
            if row:
                result.name = row["name"] or ""
                result.country = row["country"] or ""
                result.iso3 = row["iso3"] or ""
                result.lat = float(row["lat"]) if row["lat"] else 0.0
                result.lon = float(row["lon"]) if row["lon"] else 0.0
                result.port_id = (
                    row["source_payload"].get("portid", "")
                    if isinstance(row["source_payload"], dict) else ""
                )
                result.port_type = "port"

                # ⭐ FIX #1: Extract industry profile from source_payload
                profile = _extract_profile(row["source_payload"])
                result.industry_profile = profile

            if not result.name:
                # Try chokepoints
                row = await conn.fetchrow(
                    """SELECT id, name, country,
                              ST_X(geom) AS lon, ST_Y(geom) AS lat,
                              source_payload
                       FROM chokepoints
                       WHERE source_payload->>'pageid' = $1
                          OR source_payload->>'portid' = $1
                       LIMIT 1""",
                    pageid,
                )
                if row:
                    result.name = row["name"] or ""
                    result.port_type = "chokepoint"
                    result.lat = float(row["lat"]) if row["lat"] else 0.0
                    result.lon = float(row["lon"]) if row["lon"] else 0.0

            # Try live ArcGIS fallback (FIX #5: pageid -> portid mapping)
            if not result.industry_profile or not result.industry_profile.get("industry_top1"):
                arc_profile = await get_port_industry(pageid)
                if arc_profile:
                    result.industry_profile = arc_profile
                    result.port_id = arc_profile.get("portid", result.port_id)
                    result.name = result.name or arc_profile.get("portname", "")

            # Build metrics with available data
            metrics = await _build_metrics(conn, pageid, result.industry_profile)
            result.metrics = metrics

            # Build time-series
            ts = await _build_timeseries(conn, pageid)
            result.timeseries = ts

    except Exception as exc:
        logger.warning("PortWatch page error for %s: %s", pageid, exc)

    # Fallback: if no DB data and no ArcGIS data, try local CSV
    if not result.name:
        local = get_local_port(pageid)
        if local:
            result.name = local.get("portname", "") or local.get("fullname", "")
            result.country = local.get("country", "")
            result.lat = float(local.get("lat", 0))
            result.lon = float(local.get("lon", 0))
            result.port_id = local.get("portid", "")
            result.pageid = local.get("pageid", pageid)
            # Extract industry profile from CSV columns
            profile = {
                "industry_top1": local.get("industry_top1", ""),
                "industry_top2": local.get("industry_top2", ""),
                "industry_top3": local.get("industry_top3", ""),
                "portid": local.get("portid", ""),
                "portname": local.get("portname", ""),
                "country": local.get("country", ""),
            }
            result.industry_profile = profile
            # Rebuild metrics with industry data
            metrics = await _build_metrics(conn, pageid, profile)
            result.metrics = metrics

    return result
# ── Search ────────────────────────────────────────────────


@router.get("/search", response_model=SearchResult)
async def search_portwatch(q: str = Query(..., description="Search query")):
    """Search ports and chokepoints by name or pageid."""
    like = f"%{q.lower()}%"
    results: list[PortBrief] = []

    try:
        async with pg_conn() as conn:
            # Ports
            rows = await conn.fetch(
                """SELECT source_payload->>'pageid' AS pageid,
                          source_payload->>'portid' AS portid,
                          name,
                          COALESCE(country, '') AS country,
                          COALESCE(iso3, '') AS iso3,
                          ST_X(geom) AS lon, ST_Y(geom) AS lat
                   FROM ports
                   WHERE LOWER(name) LIKE $1
                      OR LOWER(source_payload->>'pageid') LIKE $1
                      OR LOWER(source_payload->>'portname') LIKE $1
                   LIMIT 10""",
                like,
            )
            for row in rows:
                results.append(
                    PortBrief(
                        pageid=row["pageid"] or "",
                        portid=row["portid"] or "",
                        name=row["name"] or "",
                        country=row["country"] or "",
                        iso3=row["iso3"] or "",
                        lat=float(row["lat"]) if row["lat"] else 0.0,
                        lon=float(row["lon"]) if row["lon"] else 0.0,
                        port_type="port",
                        url=f"https://portwatch.imf.org/pages/{row['pageid'] or ''}",
                    )
                )

            # Chokepoints
            rows2 = await conn.fetch(
                """SELECT source_payload->>'pageid' AS pageid,
                          source_payload->>'portid' AS portid,
                          name,
                          '' AS country, '' AS iso3,
                          ST_X(geom) AS lon, ST_Y(geom) AS lat
                   FROM chokepoints
                   WHERE LOWER(name) LIKE $1
                      OR LOWER(source_payload->>'pageid') LIKE $1
                   LIMIT 10""",
                like,
            )
            for row in rows2:
                results.append(
                    PortBrief(
                        pageid=row["pageid"] or "",
                        portid=row["portid"] or "",
                        name=row["name"] or "",
                        lat=float(row["lat"]) if row["lat"] else 0.0,
                        lon=float(row["lon"]) if row["lon"] else 0.0,
                        port_type="chokepoint",
                        url=f"https://portwatch.imf.org/pages/{row['pageid'] or ''}",
                    )
                )
    except Exception as exc:
        logger.warning("Search error: %s", exc)

    # If no DB results, fall back to local CSV data
    if not results:
        for entry in search_local(q):
            results.append(
                PortBrief(
                    pageid=entry.get("pageid", ""),
                    portid=entry.get("portid", ""),
                    name=entry.get("portname", "") or entry.get("fullname", ""),
                    country=entry.get("country", ""),
                    iso3=entry.get("iso3", ""),
                    lat=float(entry.get("lat", 0)),
                    lon=float(entry.get("lon", 0)),
                    port_type=entry.get("port_type", "port"),
                    url=f"https://portwatch.imf.org/pages/{entry.get('pageid', '')}",
                )
            )

    return SearchResult(query=q, results=results)


# ── Helpers ────────────────────────────────────────────────


def _extract_profile(payload: Any) -> dict[str, Any]:
    """Extract industry profile from JSONB source_payload."""
    if isinstance(payload, str):
        payload = json.loads(payload)
    if not isinstance(payload, dict):
        return {}
    return {
        "portid": payload.get("portid", ""),
        "portname": payload.get("portname", "") or payload.get("fullname", ""),
        "country": payload.get("country", ""),
        "industry_top1": payload.get("industry_top1", ""),
        "industry_top2": payload.get("industry_top2", ""),
        "industry_top3": payload.get("industry_top3", ""),
        "industry_code1": payload.get("industry_code1", ""),
        "industry_code2": payload.get("industry_code2", ""),
        "industry_code3": payload.get("industry_code3", ""),
    }


async def _build_metrics(
    conn, pageid: str, profile: dict[str, Any]
) -> PortWatchMetrics:
    """Build PortWatchMetrics from available DB data."""
    metrics = PortWatchMetrics()
    if profile:
        tops = [
            profile.get(f"industry_top{i}", "")
            for i in (1, 2, 3)
            if profile.get(f"industry_top{i}", "")
        ]
        metrics.top_industries = tops
        metrics.primary_industry = tops[0] if tops else ""

    # Try to get vessel counts from AIS data
    try:
        row = await conn.fetchrow(
            """SELECT COUNT(*) AS total,
                      COUNT(DISTINCT mmsi) AS unique_ships
               FROM traffic_logs
               WHERE position IS NOT NULL
                 AND ST_DWithin(
                     position::geography,
                     (SELECT geom FROM ports WHERE source_payload->>'pageid' = $1 LIMIT 1)::geography,
                     5000
                 )
                 AND event_time >= now() - interval '10 minutes'""",
            pageid,
        )
        if row:
            metrics.total_vessels = row["unique_ships"] or 0
            metrics.total_portcalls = row["total"] or 0
    except Exception:
        pass

    return metrics


async def _build_timeseries(conn, pageid: str) -> PortWatchTimeSeries:
    """Build time-series from portwatch_timeseries table."""
    ts = PortWatchTimeSeries()
    try:
        rows = await conn.fetch(
            """SELECT dataset_name, date_label, value
               FROM portwatch_timeseries
               WHERE pageid = $1
               ORDER BY date_label ASC""",
            pageid,
        )
        for row in rows:
            pt = TimeSeriesPoint(date=row["date_label"], value=float(row["value"]))
            name = row["dataset_name"]
            if name in ("portcalls", "vessel_traffic_monthly"):
                ts.portcalls.append(pt)
            elif name in ("imports", "trade_imports"):
                ts.imports.append(pt)
            elif name in ("exports", "trade_exports"):
                ts.exports.append(pt)
    except Exception:
        pass
    return ts