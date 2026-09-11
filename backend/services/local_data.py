"""Local data fallback — loads CSV/GeoJSON files when Postgres is unavailable.

Enables search and port data to work without a running Postgres instance.
"""
from __future__ import annotations

import csv
import json
import logging
from pathlib import Path
from typing import Any

logger = logging.getLogger(__name__)

DATA_DIR = Path(__file__).resolve().parent.parent.parent / "data"

# In-memory storage
_ports: list[dict[str, Any]] = []
_chokepoints: list[dict[str, Any]] = []
_port_index: dict[str, dict[str, Any]] = {}  # keyed by pageid, portid, name
_loaded = False


def load_local_data(force: bool = False) -> None:
    """Load all CSV and GeoJSON files into memory."""
    global _ports, _chokepoints, _port_index, _loaded
    if _loaded and not force:
        return

    _ports = []
    _chokepoints = []
    _port_index = {}

    # -- Ports.csv --
    ports_csv = DATA_DIR / "Ports.csv"
    if ports_csv.exists():
        try:
            with open(ports_csv) as f:
                reader = csv.DictReader(f)
                for row in reader:
                    entry = {
                        "portid": row.get("portid", ""),
                        "portname": row.get("portname", "") or row.get("name", ""),
                        "fullname": row.get("fullname", ""),
                        "country": row.get("country", ""),
                        "iso3": row.get("ISO3", row.get("iso3", "")),
                        "pageid": row.get("pageid", ""),
                        "lat": _float(row, "lat"),
                        "lon": _float(row, "lon"),
                        "industry_top1": row.get("industry_top1", ""),
                        "industry_top2": row.get("industry_top2", ""),
                        "industry_top3": row.get("industry_top3", ""),
                        "port_type": "port",
                    }
                    _ports.append(entry)
                    for key in (entry["pageid"], entry["portid"], entry["portname"].lower()):
                        if key:
                            _port_index[key] = entry
        except Exception as e:
            logger.warning("Failed to load %s: %s", ports_csv, e)

    # -- Chokepoints.csv --
    chokepoints_csv = DATA_DIR / "PortWatch_Chokepoints_database.csv"
    if chokepoints_csv.exists():
        try:
            with open(chokepoints_csv) as f:
                reader = csv.DictReader(f)
                for row in reader:
                    entry = {
                        "portid": row.get("portid", ""),
                        "portname": row.get("portname", "") or row.get("name", ""),
                        "country": row.get("country", ""),
                        "pageid": row.get("pageid", ""),
                        "lat": _float(row, "lat"),
                        "lon": _float(row, "lon"),
                        "port_type": "chokepoint",
                    }
                    _chokepoints.append(entry)
                    for key in (entry["pageid"], entry["portid"], entry["portname"].lower()):
                        if key:
                            _port_index[key] = entry
        except Exception as e:
            logger.warning("Failed to load %s: %s", chokepoints_csv, e)

    logger.info("Local data loaded: %d ports, %d chokepoints", len(_ports), len(_chokepoints))
    _loaded = True


def _float(row: dict, key: str) -> float:
    try:
        return float(row.get(key, 0) or 0)
    except (ValueError, TypeError):
        return 0.0


def search_local(query: str) -> list[dict[str, Any]]:
    """Search ports and chokepoints from in-memory CSV data."""
    load_local_data()
    q = query.lower().strip()
    if not q:
        return []

    results = []
    for entry in _ports + _chokepoints:
        name = entry.get("portname", "").lower()
        pageid = entry.get("pageid", "").lower()
        portid = entry.get("portid", "").lower()
        country = entry.get("country", "").lower()
        if q in name or q in pageid or q in portid or q in country:
            results.append(entry)
    return results[:20]


def get_local_port(pageid_or_name: str) -> dict[str, Any] | None:
    """Look up a single port by pageid, portid or name."""
    load_local_data()
    key = pageid_or_name.lower()
    if key in _port_index:
        return _port_index[key]
    # Fuzzy match by name
    for entry in _ports + _chokepoints:
        if key in entry.get("portname", "").lower():
            return entry
    return None


def get_all_ports_geojson() -> dict[str, Any]:
    """Return all ports as a GeoJSON FeatureCollection."""
    load_local_data()
    features = []
    for p in _ports:
        lat, lon = p.get("lat"), p.get("lon")
        if lat and lon:
            features.append({
                "type": "Feature",
                "geometry": {"type": "Point", "coordinates": [lon, lat]},
                "properties": {
                    "name": p.get("portname", ""),
                    "pageid": p.get("pageid", ""),
                    "portid": p.get("portid", ""),
                    "country": p.get("country", ""),
                    "port_type": "port",
                },
            })
    return {"type": "FeatureCollection", "features": features}


def get_all_chokepoints_geojson() -> dict[str, Any]:
    """Return all chokepoints as a GeoJSON FeatureCollection."""
    load_local_data()
    features = []
    for p in _chokepoints:
        lat, lon = p.get("lat"), p.get("lon")
        if lat and lon:
            features.append({
                "type": "Feature",
                "geometry": {"type": "Point", "coordinates": [lon, lat]},
                "properties": {
                    "name": p.get("portname", ""),
                    "pageid": p.get("pageid", ""),
                    "port_type": "chokepoint",
                },
            })
    return {"type": "FeatureCollection", "features": features}