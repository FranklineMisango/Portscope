#!/usr/bin/env python3
"""Pump ArcGIS historical data into the portwatch_timeseries table.

This script fixes Go Failure #2: the ``portwatch_timeseries`` table
existed but was never populated by any ingest process.

It queries the ArcGIS FeatureServer for every known port's historical
data (portcalls, imports, exports per date) and writes it into the
``portwatch_timeseries`` table so the API can serve chart data.
"""
from __future__ import annotations

import asyncio
import logging
import os
import sys
import time

import asyncpg

from config import settings
from services.arcgis import PORTS_URL, _fetch_arcgis

logging.basicConfig(level=logging.INFO, format="%(asctime)s [%(levelname)s] %(name)s: %(message)s")
logger = logging.getLogger("pump_timeseries")


async def get_all_port_ids() -> list[dict[str, str]]:
    """Get all port pageid + portid pairs from the local DB."""
    dsn = settings.POSTGRES_DSN
    if dsn.startswith("postgresql+asyncpg://"):
        dsn = dsn.replace("postgresql+asyncpg://", "postgresql://")
    conn = await asyncpg.connect(dsn=dsn)
    try:
        rows = await conn.fetch(
            """SELECT DISTINCT
                      source_payload->>'pageid' AS pageid,
                      source_payload->>'portid' AS portid,
                      source_payload->>'portname' AS portname
               FROM ports
               WHERE source_payload->>'pageid' IS NOT NULL
                  OR source_payload->>'portid' IS NOT NULL
               LIMIT 50"""
        )
        return [
            {"pageid": r["pageid"] or "", "portid": r["portid"] or "",
             "portname": r["portname"] or ""}
            for r in rows
        ]
    finally:
        await conn.close()


async def pump_port_history(port: dict[str, str]) -> int:
    """Fetch historical data for one port and write to portwatch_timeseries."""
    port_id = port.get("portid") or port.get("pageid", "")
    if not port_id:
        return 0

    features = await _fetch_arcgis(
        PORTS_URL,
        where=f"portid = '{port_id}'",
        out_fields="date,portcalls,import,export,n_total",
        return_geometry="false",
        max_records=500,
    )

    if not features:
        logger.info("  No historical data for %s", port.get("portname", port_id))
        return 0

    dsn = settings.POSTGRES_DSN
    if dsn.startswith("postgresql+asyncpg://"):
        dsn = dsn.replace("postgresql+asyncpg://", "postgresql://")
    conn = await asyncpg.connect(dsn=dsn)
    try:
        written = 0
        for feat in features:
            date = feat.get("date", "")
            if not date:
                continue

            pageid = port["pageid"]

            # Portcalls
            pc = feat.get("portcalls", 0) or feat.get("n_total", 0)
            if pc and int(pc) > 0:
                await conn.execute(
                    """INSERT INTO portwatch_timeseries
                       (pageid, dataset_name, date_label, value)
                       VALUES ($1, 'portcalls', $2, $3)
                       ON CONFLICT (pageid, dataset_name, date_label)
                       DO UPDATE SET value = EXCLUDED.value""",
                    pageid, date, float(pc),
                )
                written += 1

            # Imports
            imp = feat.get("import", 0)
            if imp and float(imp) > 0:
                await conn.execute(
                    """INSERT INTO portwatch_timeseries
                       (pageid, dataset_name, date_label, value)
                       VALUES ($1, 'imports', $2, $3)
                       ON CONFLICT (pageid, dataset_name, date_label)
                       DO UPDATE SET value = EXCLUDED.value""",
                    pageid, date, float(imp),
                )
                written += 1

            # Exports
            exp = feat.get("export", 0)
            if exp and float(exp) > 0:
                await conn.execute(
                    """INSERT INTO portwatch_timeseries
                       (pageid, dataset_name, date_label, value)
                       VALUES ($1, 'exports', $2, $3)
                       ON CONFLICT (pageid, dataset_name, date_label)
                       DO UPDATE SET value = EXCLUDED.value""",
                    pageid, date, float(exp),
                )
                written += 1

        logger.info("  Wrote %d rows for %s", written, port.get("portname", port_id))
        return written
    finally:
        await conn.close()


async def main():
    logger.info("Starting timeseries pump...")

    ports = await get_all_port_ids()
    logger.info("Found %d ports to process", len(ports))

    total = 0
    for i, port in enumerate(ports, 1):
        logger.info("[%d/%d] %s", i, len(ports), port.get("portname", port.get("portid", "?")))
        written = await pump_port_history(port)
        total += written
        await asyncio.sleep(0.5)  # Rate limit

    logger.info("Done! Wrote %d total time-series rows", total)


if __name__ == "__main__":
    asyncio.run(main())