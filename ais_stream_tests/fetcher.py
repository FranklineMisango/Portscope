#!/usr/bin/env python3
"""
AIS Ship Data Fetcher & Ingester
=================================
Connects to aisstream.io (global bounding box), collects AIS positions + static data,
and writes enriched data to PostgreSQL for the live analytics pipeline.

Usage:
    python fetcher.py                        # Global fetch, 10 mins -> CSV + PostgreSQL
    python fetcher.py --duration 300         # Custom duration
    python fetcher.py --db-only              # Only write to DB, no file save
    python fetcher.py --file-only            # Only save file, no DB
    python fetcher.py --bbox buenos_aires    # Specific bounding box
"""

import asyncio
import websockets
import json
import pandas as pd
import numpy as np
import argparse
import os
import sys
import time
import math
from datetime import datetime, timezone
from pathlib import Path
from typing import Optional

# =============================================================================
# Preset bounding boxes
# =============================================================================
PRESET_BOXES = {
    "buenos_aires": [[-36.0, -59.0], [-34.0, -56.0]],
    "san_francisco": [[36.5, -124.0], [38.5, -121.5]],
    "global": [[-90, -180], [90, 180]],
}

# =============================================================================
# Lookup tables
# =============================================================================

NAV_STATUS_MAP = {
    0: "Under way using engine", 1: "At anchor", 2: "Not under command",
    3: "Restricted manoeuvrability", 4: "Constrained by draught", 5: "Moored",
    6: "Aground", 7: "Engaged in fishing", 8: "Under way sailing",
    9: "Reserved", 10: "Reserved", 11: "Towing astern", 12: "Pushing ahead",
    13: "Reserved", 14: "AIS-SART (MOB)", 15: "Not defined",
}

SHIP_TYPE_CATEGORIES = {
    20: "Wing in ground", 30: "Fishing", 31: "Towing", 32: "Towing large",
    33: "Dredging", 34: "Diving", 35: "Military", 36: "Sailing",
    37: "Pleasure craft", 40: "High speed craft", 50: "Pilot vessel",
    51: "Search and rescue", 52: "Tug", 53: "Port tender",
    54: "Anti-pollution", 55: "Law enforcement", 58: "Medical transport",
    60: "Passenger", 70: "Cargo", 80: "Tanker", 90: "Other",
}

def categorize_ship_type(code: Optional[int]) -> str:
    if code is None:
        return ""
    code = int(code)
    base = (code // 10) * 10
    return SHIP_TYPE_CATEGORIES.get(base, f"Type {code}")

def load_api_key() -> str:
    api_key = os.environ.get("AISAPIKEY")
    if api_key:
        return api_key
    env_file = Path(__file__).with_name(".env")
    if env_file.exists():
        for line in env_file.read_text().splitlines():
            line = line.strip()
            if not line or line.startswith("#") or "=" not in line:
                continue
            key, value = line.split("=", 1)
            if key.strip() == "AISAPIKEY":
                api_key = value.strip().strip('"').strip("'")
                if api_key:
                    os.environ["AISAPIKEY"] = api_key
                    return api_key
    raise RuntimeError("AISAPIKEY not set. Add to .env or export AISAPIKEY=xxx")

def get_db_connection():
    """Get PostgreSQL connection if available."""
    dsn = os.environ.get("POSTGRES_DSN", "postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable")
    try:
        import psycopg2
        conn = psycopg2.connect(dsn)
        conn.autocommit = False
        return conn
    except Exception as e:
        print(f"  No DB connection: {e}")
        return None

def insert_batch(conn, records):
    """Batch insert AIS positions into PostgreSQL."""
    if conn is None or len(records) == 0:
        return
    try:
        cur = conn.cursor()
        for r in records:
            cur.execute(
                """INSERT INTO ais_positions 
                (port_id, mmsi, ship_name, ship_type, call_sign, imo_number,
                 latitude, longitude, speed_knots, course_over_ground, true_heading,
                 nav_status, nav_status_code, rate_of_turn, destination,
                 ship_length_m, ship_width_m, draught, message_type, timestamp)
                VALUES (%s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s,
                        %s, %s, %s, %s, %s, %s, %s, %s, %s)""",
                (
                    r.get("port", ""),
                    r.get("mmsi", 0),
                    r.get("ship_name", ""),
                    r.get("ship_type", ""),
                    r.get("call_sign", ""),
                    r.get("imo_number"),
                    r.get("latitude"),
                    r.get("longitude"),
                    r.get("speed_knots", 0),
                    r.get("course_over_ground", 0),
                    r.get("true_heading"),
                    r.get("nav_status", ""),
                    r.get("nav_status_code"),
                    r.get("rate_of_turn"),
                    r.get("destination", ""),
                    r.get("ship_length_m"),
                    r.get("ship_width_m"),
                    r.get("draught"),
                    r.get("message_type", "PositionReport"),
                    r.get("timestamp"),
                ),
            )
        conn.commit()
        cur.close()
    except Exception as e:
        print(f"  DB insert error: {e}")
        conn.rollback()

def update_ship_cache(conn, ship_cache):
    """Upsert ship static data into PostgreSQL."""
    if conn is None or len(ship_cache) == 0:
        return
    try:
        cur = conn.cursor()
        for mmsi, info in ship_cache.items():
            cur.execute(
                """INSERT INTO ship_cache (mmsi, ship_name, call_sign, imo_number, ship_type, ship_type_code,
                   ship_length_m, ship_width_m, draught, destination, last_seen)
                VALUES (%s, %s, %s, %s, %s, %s, %s, %s, %s, %s, NOW())
                ON CONFLICT (mmsi) DO UPDATE SET
                    ship_name = EXCLUDED.ship_name,
                    ship_type = EXCLUDED.ship_type,
                    last_seen = NOW()""",
                (
                    mmsi,
                    info.get("ship_name", ""),
                    info.get("call_sign", ""),
                    info.get("imo_number"),
                    info.get("ship_type", ""),
                    info.get("ship_type_code"),
                    info.get("ship_length_m"),
                    info.get("ship_width_m"),
                    info.get("draught"),
                    info.get("destination", ""),
                ),
            )
        conn.commit()
        cur.close()
    except Exception as e:
        print(f"  Ship cache update error: {e}")
        conn.rollback()


async def fetch_ais_data(
    api_key: str,
    bbox_name: str = "global",
    duration_seconds: int = 600,
    db_conn=None,
    save_file: bool = True,
    output_dir: str = "./ais_data",
) -> pd.DataFrame:
    """
    Connect to AIS stream and collect enriched position data.
    """
    bbox = PRESET_BOXES.get(bbox_name, PRESET_BOXES["global"])
    print(f"Bounding box: {bbox_name}")
    print(f"Duration: {duration_seconds}s ({duration_seconds/60:.1f} min)")
    print()

    records = []
    ship_static_cache = {}
    static_count = 0
    position_count = 0
    start_time = datetime.now(timezone.utc)
    last_db_flush = time.time()

    async with websockets.connect("wss://stream.aisstream.io/v0/stream") as websocket:
        subscribe_message = {
            "APIKey": api_key,
            "BoundingBoxes": [bbox],
            "FilterMessageTypes": [
                "PositionReport",
                "ShipStaticData",
                "StandardClassBPositionReport",
                "ExtendedClassBPositionReport",
            ],
        }

        await websocket.send(json.dumps(subscribe_message))
        print("Subscription sent. Listening...")

        try:
            async for message_json in websocket:
                raw_data = json.loads(message_json)

                if "error" in raw_data:
                    print(f"Server error: {raw_data['error']}")
                    break

                msg_type = raw_data.get("MessageType")
                meta = raw_data.get("MetaData", {})
                mmsi = meta.get("MMSI", 0)

                # ---- ShipStaticData: cache for enrichment ----
                if msg_type == "ShipStaticData":
                    static = raw_data["Message"]["ShipStaticData"]
                    dims = static.get("Dimension", {})
                    ship_info = {
                        "ship_name": meta.get("ShipName", static.get("Name", "")),
                        "call_sign": static.get("CallSign", ""),
                        "imo_number": static.get("ImoNumber"),
                        "ship_type_code": static.get("Type"),
                        "ship_type": categorize_ship_type(static.get("Type")),
                        "destination": static.get("Destination", "").replace("@", "").strip(),
                        "draught": static.get("MaximumStaticDraught"),
                        "ship_length_m": (dims.get("A") or 0) + (dims.get("B") or 0) if dims.get("A") else None,
                        "ship_width_m": (dims.get("C") or 0) + (dims.get("D") or 0) if dims.get("C") else None,
                    }
                    ship_static_cache[mmsi] = ship_info
                    static_count += 1
                    continue

                # ---- PositionReport or ClassB variants ----
                if msg_type in ("PositionReport", "StandardClassBPositionReport"):
                    msg_data = raw_data["Message"][msg_type]
                elif msg_type == "ExtendedClassBPositionReport":
                    msg_data = raw_data["Message"]["ExtendedClassBPositionReport"]
                else:
                    continue

                lat = msg_data.get("Latitude")
                lon = msg_data.get("Longitude")
                if lat is None or lon is None:
                    continue

                speed = msg_data.get("Sog", 0)
                cog = msg_data.get("Cog", 0)
                heading = msg_data.get("TrueHeading")
                nav_status_code = msg_data.get("NavigationalStatus", 15)

                static_info = ship_static_cache.get(mmsi, {})

                record = {
                    "mmsi": mmsi,
                    "ship_name": static_info.get("ship_name", meta.get("ShipName", "")),
                    "call_sign": static_info.get("call_sign", ""),
                    "imo_number": static_info.get("imo_number"),
                    "ship_type": static_info.get("ship_type", ""),
                    "ship_type_code": static_info.get("ship_type_code"),
                    "timestamp": datetime.now(timezone.utc),
                    "latitude": lat,
                    "longitude": lon,
                    "port": "",
                    "speed_knots": speed,
                    "course_over_ground": cog,
                    "true_heading": heading,
                    "nav_status_code": nav_status_code,
                    "nav_status": NAV_STATUS_MAP.get(nav_status_code, f"Unknown ({nav_status_code})"),
                    "rate_of_turn": msg_data.get("RateOfTurn"),
                    "ship_length_m": static_info.get("ship_length_m"),
                    "ship_width_m": static_info.get("ship_width_m"),
                    "draught": static_info.get("draught"),
                    "destination": static_info.get("destination", ""),
                    "message_type": msg_type,
                }
                records.append(record)
                position_count += 1

                # Flush to DB every 10s
                now = time.time()
                if db_conn and (now - last_db_flush >= 10 or position_count % 50 == 0):
                    insert_batch(db_conn, records[-50:] if len(records) > 50 else records)
                    if static_count > 0 and position_count % 100 == 0:
                        update_ship_cache(db_conn, ship_static_cache)
                    last_db_flush = now

                # Progress
                if position_count % 50 == 0:
                    elapsed = (datetime.now(timezone.utc) - start_time).total_seconds()
                    print(f"  {position_count} positions ({static_count} static) - {elapsed:.0f}s")

                # Time check
                elapsed = (datetime.now(timezone.utc) - start_time).total_seconds()
                if elapsed >= duration_seconds:
                    print(f"\nDuration reached ({duration_seconds}s).")
                    break

        except websockets.exceptions.ConnectionClosed as e:
            print(f"\nConnection closed: {e}")

    # Final DB flush
    if db_conn and len(records) > 0:
        insert_batch(db_conn, records)
        update_ship_cache(db_conn, ship_static_cache)

    print(f"\n=== Summary ===")
    print(f"  Positions: {position_count}")
    print(f"  Ships with static data: {len(ship_static_cache)}")
    print(f"  Static messages: {static_count}")

    return pd.DataFrame(records)


def main():
    parser = argparse.ArgumentParser(description="AIS Ship Data Fetcher")
    parser.add_argument("--duration", "-d", type=int, default=600)
    parser.add_argument("--output", "-o", type=str, default="./ais_data")
    parser.add_argument("--bbox", "-b", type=str, default="global",
                        choices=list(PRESET_BOXES.keys()))
    parser.add_argument("--db-only", action="store_true")
    parser.add_argument("--file-only", action="store_true")
    parser.add_argument("--no-save", action="store_true")
    args = parser.parse_args()

    api_key = load_api_key()
    print(f"API Key: {api_key[:8]}...{api_key[-4:]}")

    # Get DB connection unless --file-only
    db_conn = None
    if not args.file_only and not args.no_save:
        db_conn = get_db_connection()
        if db_conn:
            print("PostgreSQL connected")
        else:
            print("No DB - file-only mode")
            args.file_only = True

    # Fetch
    start_wall = time.time()
    df = asyncio.run(fetch_ais_data(
        api_key, args.bbox, args.duration,
        db_conn=db_conn,
        save_file=not args.db_only and not args.no_save,
        output_dir=args.output,
    ))
    wall_elapsed = time.time() - start_wall

    if db_conn:
        db_conn.close()

    if len(df) == 0:
        print("\nNo data collected.")
        return

    # Print summary
    print(f"\nDataFrame: {df.shape[0]} rows x {df.shape[1]} columns")
    print(f"Wall clock: {wall_elapsed:.1f}s")
    print(f"Unique ships: {df['mmsi'].nunique()}")

    # Enrich
    df["is_moving"] = df["speed_knots"] > 1.0
    df["speed_class"] = pd.cut(
        df["speed_knots"],
        bins=[-0.1, 0.5, 3, 10, 20, 60],
        labels=["Anchored/Moored", "Slow (<3 kts)", "Moderate (3-10)", "Fast (10-20)", "Very fast (>20)"]
    )

    # Show sample
    print("\n=== Sample ===")
    cols = [c for c in ["mmsi", "ship_name", "ship_type", "speed_knots", "destination", "nav_status"] if c in df.columns]
    print(df[cols].head(10).to_string(index=False))

    print("\n=== Ship Types ===")
    if df["ship_type"].nunique() > 1:
        print(df["ship_type"].value_counts().head(10).to_string())
    else:
        print("(no type data yet)")

    # Save files
    if not args.db_only and not args.no_save:
        out_dir = Path(args.output)
        out_dir.mkdir(parents=True, exist_ok=True)
        ts = datetime.now().strftime("%Y%m%d_%H%M%S")
        base_name = f"ais_fetch_{ts}"

        csv_path = out_dir / f"{base_name}.csv"
        df.to_csv(csv_path, index=False)
        print(f"\nCSV: {csv_path}")

        try:
            pq_path = out_dir / f"{base_name}.parquet"
            df.to_parquet(pq_path, index=False)
            print(f"Parquet: {pq_path}")
        except Exception as e:
            print(f"No parquet (install pyarrow): {e}")

    print("\nDone!")


if __name__ == "__main__":
    main()