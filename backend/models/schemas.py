"""Pydantic schemas for Portscope API responses."""
from __future__ import annotations

from datetime import datetime
from typing import Any

from pydantic import BaseModel, Field


# ── Port / Chokepoint ──────────────────────────────────────

class PortBrief(BaseModel):
    id: int | None = None
    pageid: str = ""
    portid: str = ""
    name: str
    country: str = ""
    iso3: str = ""
    lat: float = 0.0
    lon: float = 0.0
    port_type: str = "port"  # "port" or "chokepoint"
    url: str = ""


class SearchResult(BaseModel):
    query: str
    results: list[PortBrief]


# ── PortWatch Metrics ──────────────────────────────────────

class VesselBreakdown(BaseModel):
    label: str
    count: int
    share_pct: float = 0.0


class CountryShare(BaseModel):
    import_share_pct: float = 0.0
    export_share_pct: float = 0.0


class PortWatchMetrics(BaseModel):
    total_vessels: int = 0
    primary_vessel_type: str = ""
    diversity_index: float = 0.0
    vessel_breakdown: list[VesselBreakdown] = []
    top_industries: list[str] = []
    primary_industry: str = ""
    country_share: CountryShare | None = None
    total_portcalls: int = 0
    total_imports: float = 0.0
    total_exports: float = 0.0
    avg_daily_portcalls: float = 0.0
    data_range_start: str = ""
    data_range_end: str = ""


class TimeSeriesPoint(BaseModel):
    date: str
    value: float


class PortWatchTimeSeries(BaseModel):
    portcalls: list[TimeSeriesPoint] = []
    imports: list[TimeSeriesPoint] = []
    exports: list[TimeSeriesPoint] = []


class UnavailableDataItem(BaseModel):
    label: str
    description: str = ""
    external_url: str = ""


class PortWatchPageResult(BaseModel):
    pageid: str = ""
    port_id: str = ""
    name: str = ""
    country: str = ""
    iso3: str = ""
    lat: float = 0.0
    lon: float = 0.0
    port_type: str = "port"
    metrics: PortWatchMetrics | None = None
    timeseries: PortWatchTimeSeries | None = None
    external_url: str = ""
    unavailable_data: list[UnavailableDataItem] = []
    industry_profile: dict[str, Any] = {}  # NEW: ArcGIS industry_top1/2/3


# ── Traffic / AIS Analytics ────────────────────────────────

class ShipInfo(BaseModel):
    mmsi: int
    speed_knots: float = 0.0
    cog: float = 0.0
    lat: float = 0.0
    lon: float = 0.0
    last_seen: datetime | None = None
    ship_name: str = ""
    ship_type: str = ""
    destination: str = ""
    ship_length_m: float | None = None
    ship_width_m: float | None = None
    draught: float | None = None


class PortAnalytics(BaseModel):
    id: int
    name: str
    country: str = ""
    iso3: str = ""
    lookback_minutes: int = 10
    radius_meters: int = 5000
    last_updated: datetime | None = None
    total_messages: int = 0
    unique_ships: int = 0
    underway: int = 0
    anchored: int = 0
    avg_speed_knots: float = 0.0
    ships: list[ShipInfo] = []


class TrafficDayCount(BaseModel):
    day: str
    count: int


# ── ML Prediction ──────────────────────────────────────────

class PredictionInput(BaseModel):
    port_id: str
    duration_seconds: int = 600  # how long to stream AIS
    use_live_traffic: bool = True


class PredictionOutput(BaseModel):
    port_id: str
    port_name: str = ""
    baseline_forecast: float | None = None
    live_adjusted_forecast: float | None = None
    pressure_factor: float | None = None
    live_vessel_count: int = 0
    vessel_breakdown: dict[str, int] = {}
    top_industries: list[str] = []
    stream_error: str | None = None

    model_config = {"protected_namespaces": ()}


# ── WebSocket messages ─────────────────────────────────────

class WSMessage(BaseModel):
    type: str  # "select", "monitor", "snapshot", "stop"
    kind: str = ""  # "port" or "chokepoint"
    id: int = 0
    name: str = ""
    data: dict[str, Any] = {}