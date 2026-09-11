"""Traffic / AIS analytics endpoints — replaces Go api/main.go traffic routes."""
from __future__ import annotations

import logging

from fastapi import APIRouter, Query

from db import pg_conn
from models.schemas import PortAnalytics, ShipInfo, TrafficDayCount

logger = logging.getLogger(__name__)

router = APIRouter(prefix="/api/traffic", tags=["traffic"])


@router.get("/analytics/{port_name}", response_model=PortAnalytics)
async def get_port_analytics(
    port_name: str,
    lookback: int = Query(10, alias="lookback", description="Lookback minutes"),
    radius: int = Query(5000, alias="radius", description="Radius in meters"),
):
    """Get live AIS analytics for a port by name (or pageid/portid).

    Returns empty analytics when Postgres is unavailable.
    """
    async with pg_conn() as conn:
        # Resolve port
        row = await conn.fetchrow(
            """SELECT id, name, country, iso3,
                      ST_X(geom) AS lon, ST_Y(geom) AS lat
               FROM ports
               WHERE name = $1 OR source_payload->>'pageid' = $1
               LIMIT 1""",
            port_name,
        )
        if not row:
            # Try local data fallback
            from services.local_data import get_local_port
            local = get_local_port(port_name)
            if local:
                return PortAnalytics(
                    id=0,
                    name=local.get("portname", "") or local.get("fullname", port_name),
                    country=local.get("country", ""),
                )
            # No port found anywhere — return empty analytics
            return PortAnalytics(id=0, name=port_name)

        port_id = row["id"]
        lookback_interval = f"{lookback} minutes"

        # Aggregate stats
        stats = await conn.fetchrow(
            """SELECT COUNT(*) AS total_msgs,
                      COUNT(DISTINCT mmsi) AS unique_ships,
                      COUNT(*) FILTER (WHERE speed_kts > 1) AS underway,
                      COUNT(*) FILTER (WHERE speed_kts <= 1) AS anchored,
                      COALESCE(AVG(speed_kts), 0) AS avg_speed,
                      MAX(event_time) AS last_updated
               FROM traffic_logs, LATERAL (
                   SELECT geom FROM ports WHERE id = $1
               ) AS port_geom
               WHERE position IS NOT NULL
                 AND ST_DWithin(position::geography, port_geom.geom::geography, $2)
                 AND event_time >= now() - $3::interval""",
            port_id, float(radius), lookback_interval,
        )

        # Recent ships
        ships_rows = await conn.fetch(
            """SELECT DISTINCT ON (mmsi)
                      mmsi,
                      COALESCE(speed_kts, 0) AS speed_knots,
                      COALESCE(course_deg, 0) AS course_over_ground,
                      event_time,
                      ST_X(position) AS lon,
                      ST_Y(position) AS lat,
                      COALESCE(payload->'Message'->'PositionReport'->>'ShipName',
                               payload->'MetaData'->>'ShipName', '') AS ship_name,
                      COALESCE(payload->'Message'->'PositionReport'->>'ShipType',
                               payload->'MetaData'->>'ShipType', '') AS ship_type,
                      COALESCE(payload->'Message'->'PositionReport'->>'Destination',
                               payload->'MetaData'->>'Destination', '') AS destination
               FROM traffic_logs, LATERAL (
                   SELECT geom FROM ports WHERE id = $1
               ) AS port_geom
               WHERE position IS NOT NULL
                 AND ST_DWithin(position::geography, port_geom.geom::geography, $2)
                 AND event_time >= now() - $3::interval
                 AND mmsi IS NOT NULL
               ORDER BY mmsi, event_time DESC
               LIMIT 50""",
            port_id, float(radius), lookback_interval,
        )

    ships = [
        ShipInfo(
            mmsi=r["mmsi"],
            speed_knots=float(r["speed_knots"]),
            cog=float(r["course_over_ground"]),
            lat=float(r["lat"]),
            lon=float(r["lon"]),
            last_seen=r["event_time"],
            ship_name=r["ship_name"] or "",
            ship_type=r["ship_type"] or "",
            destination=r["destination"] or "",
        )
        for r in ships_rows
    ]

    return PortAnalytics(
        id=port_id,
        name=row["name"],
        country=row["country"] or "",
        iso3=row["iso3"] or "",
        lookback_minutes=lookback,
        radius_meters=radius,
        last_updated=stats["last_updated"] if stats else None,
        total_messages=stats["total_msgs"] if stats else 0,
        unique_ships=stats["unique_ships"] if stats else 0,
        underway=stats["underway"] if stats else 0,
        anchored=stats["anchored"] if stats else 0,
        avg_speed_knots=float(stats["avg_speed"]) if stats else 0.0,
        ships=ships,
    )


@router.get("/history/{port_name}", response_model=list[TrafficDayCount])
async def get_traffic_history(
    port_name: str,
    range: str = Query("30d", alias="range", description="Time range: 7d, 30d, 1y"),
    radius: int = Query(5000, alias="radius", description="Radius in meters"),
):
    """Get daily vessel traffic counts for a port.

    Returns empty list when Postgres is unavailable.
    """
    interval_map = {"7d": "7 days", "30d": "30 days", "1y": "365 days"}
    interval = interval_map.get(range, "30 days")

    try:
        async with pg_conn() as conn:
            rows = await conn.fetch(
                """SELECT date_trunc('day', event_time) AS day, count(*) AS cnt
                   FROM traffic_logs
                   WHERE position IS NOT NULL
                     AND ST_DWithin(position::geography,
                         (SELECT geom FROM ports WHERE name=$1 LIMIT 1)::geography, $2)
                     AND event_time >= now() - $3::interval
                   GROUP BY day
                   ORDER BY day""",
                port_name, float(radius), interval,
            )
        return [
            TrafficDayCount(day=r["day"].isoformat(), count=r["cnt"])
            for r in rows
        ]
    except Exception:
        return []