"""Portscope Backend — FastAPI application.

This is the Python backend that replaces the Go api/ module.
It properly connects ArcGIS ↔ PortWatch/IMF data (fixes Go failures #1-#7),
serves ML predictions, streams live AIS vessel data, and uses DuckDB for
fast historical queries. The Go ingest/ worker is kept for AIS streaming.
"""
from __future__ import annotations

import json
import logging
from contextlib import asynccontextmanager

from fastapi import FastAPI, WebSocket, WebSocketDisconnect
from fastapi.middleware.cors import CORSMiddleware

from config import settings
from db import get_async_pool, pg_conn

from api.portwatch import router as portwatch_router
from api.traffic import router as traffic_router
from api.prediction import router as prediction_router

logger = logging.getLogger(__name__)


@asynccontextmanager
async def lifespan(app: FastAPI):
    """Startup / shutdown lifecycle — gracefully handles missing Postgres."""
    logger.info("Portscope backend starting up (FastAPI + Python)")
    try:
        await get_async_pool()
        logger.info("Postgres async pool ready")
    except Exception as e:
        logger.warning("Postgres not available: %s — running in limited mode", e)
    yield
    try:
        pool = await get_async_pool()
        await pool.close()
    except Exception:
        pass
    logger.info("Portscope backend shut down")


app = FastAPI(
    title="Portscope API",
    version="2.0.0",
    description="Portwatch + AIS analytics + ML prediction backend. "
                "Migrated from Go to Python FastAPI for proper ArcGIS/PortWatch integration.",
    lifespan=lifespan,
)

app.add_middleware(
    CORSMiddleware,
    allow_origins=settings.CORS_ORIGINS,
    allow_credentials=True,
    allow_methods=["*"],
    allow_headers=["*"],
)

app.include_router(portwatch_router)
app.include_router(traffic_router)
app.include_router(prediction_router)


@app.get("/api/health")
async def health():
    return {
        "status": "ok",
        "version": "2.0.0",
        "stack": "python-fastapi",
        "ais_api_key_configured": bool(settings.AISSTREAM_API_KEY),
        "model_enabled": settings.ENABLE_MODEL,
    }

@app.websocket("/api/ws")
async def websocket_endpoint(ws: WebSocket):
    """WebSocket for live map — replaces Go api/main.go lines 1076-1198."""
    await ws.accept()
    logger.info("WebSocket client connected")
    ais_api_key = settings.AISSTREAM_API_KEY

    try:
        await _send_snapshot(ws)
    except Exception as exc:
        logger.warning("Snapshot send error: %s", exc)

    try:
        while True:
            data = await ws.receive_text()
            msg = json.loads(data)
            msg_type = msg.get("type", "")
            msg_kind = msg.get("kind", "")
            msg_name = msg.get("name", "")
            msg_id = msg.get("id", 0)

            if msg_type in ("select", "monitor"):
                if msg_kind == "port":
                    record = await _load_port_record(msg_name or msg_id)
                elif msg_kind == "chokepoint":
                    record = await _load_chokepoint_record(msg_name or msg_id)
                else:
                    record = None

                if record:
                    await ws.send_json({
                        "type": "selected_record",
                        "kind": msg_kind,
                        "id": record.get("id", 0),
                        "name": record.get("name", ""),
                        "data": record,
                    })

                if msg_type == "monitor" and ais_api_key:
                    logger.info("Monitor requested for %s: %s", msg_kind, msg_name or msg_id)

            elif msg_type in ("stop", "clear"):
                logger.info("Stop/clear received")
    except WebSocketDisconnect:
        logger.info("WebSocket client disconnected")
    except Exception as exc:
        logger.warning("WebSocket error: %s", exc)


async def _send_snapshot(ws: WebSocket):
    """Send all ports/chokepoints as GeoJSON-like snapshot.
    Falls back to local GeoJSON files when Postgres is unavailable.
    """
    ports_list = []
    chokes_list = []

    try:
        async with pg_conn() as conn:
            ports_rows = await conn.fetch(
                """SELECT id, name, country, iso3,
                          ST_AsGeoJSON(geom)::json AS geom, source_payload
                   FROM ports WHERE name IS NOT NULL LIMIT 500"""
            )
            for r in ports_rows:
                p = {"id": r["id"], "name": r["name"], "country": r["country"] or "",
                     "iso3": r["iso3"] or "", "geom": r["geom"]}
                if isinstance(r["source_payload"], dict):
                    p["pageid"] = r["source_payload"].get("pageid", "")
                ports_list.append(p)

            choke_rows = await conn.fetch(
                """SELECT id, name, country,
                          ST_AsGeoJSON(geom)::json AS geom
                   FROM chokepoints LIMIT 100"""
            )
            chokes_list = [
                {"id": r["id"], "name": r["name"], "geom": r["geom"]}
                for r in choke_rows
            ]
    except Exception:
        logger.info("pg_conn failed for snapshot, falling back to local GeoJSON")
        # Fallback: load from GeoJSON files
        from services.local_data import get_all_ports_geojson, get_all_chokepoints_geojson
        ports_gj = get_all_ports_geojson()
        chokes_gj = get_all_chokepoints_geojson()
        for i, feat in enumerate(ports_gj.get("features", [])):
            props = feat.get("properties", {})
            ports_list.append({
                "id": i, "name": props.get("portname", ""),
                "country": props.get("country", ""),
                "geom": feat.get("geometry"),
                "pageid": props.get("pageid", ""),
            })
        for i, feat in enumerate(chokes_gj.get("features", [])):
            props = feat.get("properties", {})
            chokes_list.append({
                "id": i, "name": props.get("name", ""),
                "geom": feat.get("geometry"),
            })

    await ws.send_json({"type": "snapshot", "ports": ports_list, "chokepoints": chokes_list})


async def _load_port_record(identifier: str | int) -> dict | None:
    """Load port by name (str) or id (int)."""
    async with pg_conn() as conn:
        if isinstance(identifier, str):
            row = await conn.fetchrow(
                "SELECT id, name, country, iso3, "
                "ST_AsGeoJSON(geom)::json AS geom, source_payload "
                "FROM ports WHERE name = $1 LIMIT 1", identifier)
        else:
            row = await conn.fetchrow(
                "SELECT id, name, country, iso3, "
                "ST_AsGeoJSON(geom)::json AS geom, source_payload "
                "FROM ports WHERE id = $1", identifier)
        return dict(row) if row else None


async def _load_chokepoint_record(identifier: str | int) -> dict | None:
    """Load chokepoint by name or id."""
    async with pg_conn() as conn:
        if isinstance(identifier, str):
            row = await conn.fetchrow(
                "SELECT id, name, ST_AsGeoJSON(geom)::json AS geom "
                "FROM chokepoints WHERE name = $1 LIMIT 1", identifier)
        else:
            row = await conn.fetchrow(
                "SELECT id, name, ST_AsGeoJSON(geom)::json AS geom "
                "FROM chokepoints WHERE id = $1", identifier)
        return dict(row) if row else None


if __name__ == "__main__":
    import uvicorn
    uvicorn.run(
        "main:app",
        host=settings.HOST,
        port=settings.PORT,
        reload=settings.DEBUG,
        log_level="info",
    )