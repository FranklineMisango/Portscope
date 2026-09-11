"""AISStream.io WebSocket client — live vessel data collection.

Mirrors ship_value_analysis.py's ``stream_static_data()`` (line 85–170).
"""
from __future__ import annotations

import asyncio
import json
import logging
from typing import Any, Callable

import websockets

from config import settings

logger = logging.getLogger(__name__)


async def stream_ais_snapshot(
    lat: float,
    lon: float,
    radius_km: float = 15.0,
    duration_seconds: int = 600,
    on_message: Callable[[dict[str, Any]], None] | None = None,
) -> list[dict[str, Any]]:
    """Stream AIS data for *duration_seconds* within a bounding box.

    This is the async equivalent of the Python reference's ``stream_static_data()``
    (line 85-103) but returns aggregated results instead of writing to a file.

    Args:
        lat, lon: Port centre coordinates.
        radius_km: Approximate radius in km (default 15 km ≈ ±0.135°).
        duration_seconds: How long to collect messages.
        on_message: Optional callback fired for each received message.

    Returns:
        List of parsed AIS message dicts collected during the window.
    """
    if not settings.AISSTREAM_API_KEY:
        logger.warning("AISSTREAM_API_KEY not set; using empty snapshot")
        return []

    # Compute bounding box as [[south, west], [north, east]]
    lat_delta = radius_km / 111.0  # ~1° ≈ 111 km
    lon_delta = radius_km / (111.0 * abs(__import__("math").cos(lat * 3.14159 / 180.0)) + 0.001)

    bounding_box = [
        [lat - lat_delta, lon - lon_delta],
        [lat + lat_delta, lon + lon_delta],
    ]

    collected: list[dict[str, Any]] = []
    deadline = asyncio.get_event_loop().time() + duration_seconds

    try:
        async with websockets.connect(
            "wss://stream.aisstream.io/v0/stream",
            compression="deflate",
            ping_interval=20,
            ping_timeout=10,
        ) as ws:
            # Subscribe with ShipStaticData filter (matches Python reference)
            subscribe_msg = {
                "APIKey": settings.AISSTREAM_API_KEY,
                "BoundingBoxes": [bounding_box],
                "FilterMessageTypes": [
                    "ShipStaticData",
                    "PositionReport",
                ],
            }
            await ws.send(json.dumps(subscribe_msg))
            logger.info(
                "AIS stream opened for (%.4f, %.4f) radius=%.1fkm window=%ds",
                lat, lon, radius_km, duration_seconds,
            )

            while asyncio.get_event_loop().time() < deadline:
                try:
                    payload = await asyncio.wait_for(
                        ws.recv(), timeout=deadline - asyncio.get_event_loop().time()
                    )
                    msg = json.loads(payload)
                    collected.append(msg)
                    if on_message:
                        on_message(msg)
                except asyncio.TimeoutError:
                    break
    except websockets.WebSocketException as exc:
        logger.warning("AIS WebSocket error: %s", exc)

    logger.info("AIS snapshot complete: %d messages collected", len(collected))
    return collected


def aggregate_vessels(messages: list[dict[str, Any]]) -> dict[str, Any]:
    """Aggregate AIS messages into vessel counts and breakdowns.

    Mirrors ship_value_analysis.py's aggregation logic (lines 107-170).
    """
    ships: dict[int, dict[str, Any]] = {}
    ship_types: dict[str, int] = {}
    destinations: dict[str, int] = {}

    for msg in messages:
        meta = msg.get("MetaData", {}) or {}
        message_block = msg.get("Message", {}) or {}

        # Try ShipStaticData first, then PositionReport
        ship_data = (
            message_block.get("ShipStaticData", {})
            or message_block.get("PositionReport", {})
            or {}
        )

        mmsi = meta.get("MMSI") or ship_data.get("MMSI")
        if not mmsi:
            continue

        mmsi = int(mmsi)
        if mmsi not in ships:
            ships[mmsi] = {
                "mmsi": mmsi,
                "ship_name": ship_data.get("ShipName", ship_data.get("Name", "")),
                "ship_type": ship_data.get("ShipType", ship_data.get("Type", "")),
                "destination": ship_data.get("Destination", ""),
                "length_m": ship_data.get("Dimension", {}).get("Length", 0),
                "width_m": ship_data.get("Dimension", {}).get("Width", 0),
                "draught": ship_data.get("Draught", 0),
                "lat": meta.get("latitude", meta.get("Latitude", 0)),
                "lon": meta.get("longitude", meta.get("Longitude", 0)),
            }

        # Count vessel types
        st = str(ship_data.get("ShipType", ship_data.get("Type", "Unknown")))
        ship_types[st] = ship_types.get(st, 0) + 1

        # Count destinations
        dest = str(ship_data.get("Destination", ""))
        if dest and dest != "None":
            destinations[dest] = destinations.get(dest, 0) + 1

    # Vessel breakdown with percentages
    total = len(ships)
    breakdown = [
        {"label": t, "count": c, "share_pct": round(c / total * 100, 1) if total else 0}
        for t, c in sorted(ship_types.items(), key=lambda x: -x[1])
    ]

    return {
        "total_vessels": total,
        "ships": list(ships.values()),
        "vessel_breakdown": breakdown,
        "primary_vessel_type": breakdown[0]["label"] if breakdown else "",
        "diversity_index": len(ship_types) / max(total, 1),
        "destination_summary": dict(sorted(destinations.items(), key=lambda x: -x[1])[:10]),
    }