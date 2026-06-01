To scale your AIS ingestion pipeline from a basic streaming script into a resilient, production-ready analytics engine, you need a decoupled architecture. Writing data sequentially to ClickHouse from an open WebSocket loop will result in dropped messages, data corruption during restarts, and severe database fragmentation.

This comprehensive architecture proposal details how to ingest, buffer, store, and analyze both `PositionReport` and `ShipStaticData` messages to calculate hourly port arrivals, departures, and cargo insights safely and efficiently.

---

## 1. System Architecture Diagram

```
 [ aisstream.io ] (External WebSocket Stream)
        │
        ▼ (TLS Connection)
 ┌─────────────────────────────────────────────────────────┐
 │ 1. INGESTION LAYER (Python asyncio Worker)             │
 │    • Manages persistent wss connection                 │
 │    • Thread-safe local memory buffer (Deque)            │
 └─────────────────────────┬───────────────────────────────┘
                           │ (In-Memory Micro-batches)
                           ▼
 ┌─────────────────────────────────────────────────────────┐
 │ 2. BUFFER & COMPACTION LAYER                            │
 │    • Flush Criteria: 5,000 records OR 10 seconds       │
 │    • Thread-safe dual-target batch router               │
 └─────────────────────────┬───────────────────────────────┘
                           │ (Bulk Vector Inserts)
                           ▼
 ┌─────────────────────────────────────────────────────────┐
 │ 3. STORAGE LAYER (ClickHouse DB Cluster)                │
 │    • vessel_positions (MergeTree - Immutable log)       │
 │    • vessel_metadata  (ReplacingMergeTree - State tracking)
 └─────────────────────────┬───────────────────────────────┘
                           │ (Hourly Cron / Vectorized Reads)
                           ▼
 ┌─────────────────────────────────────────────────────────┐
 │ 4. ANALYTICS & COMPUTED VIEW LAYER                      │
 │    • Port Geofence Polygons                             │
 │    • Materialized Views & Aggregate Lookups            │
 └─────────────────────────────────────────────────────────┘

```

---

## 2. Updated Database Schema Definition

We separate high-frequency geospatial tracking from low-frequency structural metadata. We leverage ClickHouse's `ReplacingMergeTree` for metadata, which automatically handles deduplication by keeping only the newest record for each ship (`ship_id`).

```sql
CREATE DATABASE IF NOT EXISTS vessels_tracking;

-- Table 1: High-speed append-only stream for movements
CREATE TABLE IF NOT EXISTS vessels_tracking.vessel_positions (
    ts DateTime64(3, 'UTC'),
    ship_id UInt32,
    latitude Float32,
    longitude Float32,
    speed Float32,
    heading Float32,
    nav_status UInt8
) ENGINE = MergeTree()
ORDER BY (ship_id, ts)
SETTINGS index_granularity = 8192;

-- Table 2: Stateful entity tracking for vessel context
CREATE TABLE IF NOT EXISTS vessels_tracking.vessel_metadata (
    ts DateTime64(3, 'UTC'),
    ship_id UInt32,
    ship_name String,
    ship_type UInt8,       -- Standard Marine Code (e.g., 70=Cargo, 80=Tanker)
    destination String,    -- Destination string set by the captain
    draught Float32        -- Current draught depth in meters
) ENGINE = ReplacingMergeTree(ts)
ORDER BY ship_id;

```

---

## 3. Production Python Implementation (With Micro-Batching)

This script features auto-reconnection logic, isolates the WebSocket from database blockages, and micro-batches records in memory. It tracks up to 5,000 items or waits a maximum of 10 seconds before flushing to ClickHouse.

```python
import asyncio
import websockets
import json
import os
import logging
from datetime import datetime, timezone
from clickhouse_driver import Client

# Configure production logging
logging.basicConfig(level=logging.INFO, format="%(asctime)s [%(levelname)s] %(message)s")
logger = logging.getLogger("AIS_Ingestion")

# Configuration
API_KEY = os.environ.get("AISAPIKEY")
CLICKHOUSE_HOST = os.environ.get("CLICKHOUSE_HOST", "localhost")
CH_USER = os.environ.get("CLICKHOUSE_USER")
CH_PASSWORD = os.environ.get("CLICKHOUSE_PASSWORD")

BATCH_SIZE = 5000
BATCH_TIMEOUT_SECS = 10

if not all([API_KEY, CH_USER, CH_PASSWORD]):
    raise RuntimeError("Missing crucial environment variables: AISAPIKEY, CLICKHOUSE_USER, or CLICKHOUSE_PASSWORD")

client = Client(host=CLICKHOUSE_HOST, user=CH_USER, password=CH_PASSWORD)

# Memory Buffers
position_buffer = []
metadata_buffer = []
last_flush_time = datetime.now(timezone.utc)

def flush_buffers():
    global position_buffer, metadata_buffer, last_flush_time
    now = datetime.now(timezone.utc)
    
    # 1. Flush Positions
    if position_buffer:
        try:
            insert_pos_query = "INSERT INTO vessels_tracking.vessel_positions (ts, ship_id, latitude, longitude, speed, heading, nav_status) VALUES"
            client.execute(insert_pos_query, position_buffer)
            logger.info(f"Successfully flushed {len(position_buffer)} positions to ClickHouse.")
            position_buffer.clear()
        except Exception as e:
            logger.error(f"Failed to flush position records to ClickHouse: {e}")

    # 2. Flush Metadata
    if metadata_buffer:
        try:
            insert_meta_query = "INSERT INTO vessels_tracking.vessel_metadata (ts, ship_id, ship_name, ship_type, destination, draught) VALUES"
            client.execute(insert_meta_query, metadata_buffer)
            logger.info(f"Successfully flushed {len(metadata_buffer)} metadata profiles to ClickHouse.")
            metadata_buffer.clear()
        except Exception as e:
            logger.error(f"Failed to flush metadata records to ClickHouse: {e}")
            
    last_flush_time = now

async def buffer_monitor():
    """Periodic task ensuring data does not stale in memory if traffic drops below BATCH_SIZE."""
    while True:
        await asyncio.sleep(1)
        time_since_flush = (datetime.now(timezone.utc) - last_flush_time).total_seconds()
        if time_since_flush >= BATCH_TIMEOUT_SECS and (position_buffer or metadata_buffer):
            logger.info("Batch timeout reached. Initiating timed flush...")
            flush_buffers()

async def connect_ais_stream():
    subscribe_message = {
        "APIKey": API_KEY,
        "BoundingBoxes": [
            [[-34.811548, -58.537903], [-34.284453, -57.749634]], # Buenos Aires
            [[36.989391, -123.832397], [38.449287, -121.744995]], # San Francisco
        ],
        "FilterMessageTypes": ["PositionReport", "ShipStaticData"],
    }

    while True:
        try:
            logger.info("Opening secure connection to aisstream.io...")
            async with websockets.connect("wss://stream.aisstream.io/v0/stream", ping_interval=20, ping_timeout=20) as websocket:
                await websocket.send(json.dumps(subscribe_message))
                logger.info("Subscription setup initialized successfully.")

                async for message_json in websocket:
                    msg = json.loads(message_json)
                    msg_type = msg.get("MessageType")
                    meta = msg.get("MetaData", {})
                    
                    # Core unique identifier
                    ship_id = meta.get("MMSI")
                    if not ship_id:
                        continue

                    ts_now = datetime.now(timezone.utc)

                    if msg_type == "PositionReport":
                        pos_payload = msg["Message"]["PositionReport"]
                        position_buffer.append((
                            ts_now,
                            int(ship_id),
                            float(pos_payload["Latitude"]),
                            float(pos_payload["Longitude"]),
                            float(pos_payload["Sog"]),
                            float(pos_payload["Cog"]),
                            int(pos_payload["NavigationalStatus"])
                        ))

                    elif msg_type == "ShipStaticData":
                        static_payload = msg["Message"]["ShipStaticData"]
                        metadata_buffer.append((
                            ts_now,
                            int(ship_id),
                            str(meta.get("ShipName", "UNKNOWN")).strip(),
                            int(static_payload.get("Type", 0)),
                            str(static_payload.get("Destination", "UNKNOWN")).strip(),
                            float(static_payload.get("MaximumStaticDraught", 0.0))
                        ))

                    # Inline volume execution check
                    if (len(position_buffer) + len(metadata_buffer)) >= BATCH_SIZE:
                        flush_buffers()

        except (websockets.exceptions.ConnectionClosed, Exception) as exc:
            logger.error(f"Network drop or pipeline exception encountered: {exc}. Reconnecting in 5 seconds...")
            await asyncio.sleep(5)

async def main():
    # Run stream connection alongside buffer timer monitor concurrently
    await asyncio.gather(connect_ais_stream(), buffer_monitor())

if __name__ == "__main__":
    try:
        asyncio.run(main())
    except KeyboardInterrupt:
        logger.info("Process manually terminated by operator. Saving remaining memory frames...")
        flush_buffers()

```

---

## 4. Analytical Methodology & Implementation

### A. Categorizing Ship Types & Cargo Capabilities

Because AIS does not broadcast explicit cargo line manifests, we analyze cargo types indirectly via international marine type integers (`ship_type`) combined with physical displacement variations (`draught` depth fluctuations):

| `ship_type` Base Code | Category | Analytical Context |
| --- | --- | --- |
| **`70` to `79**` | Cargo Carriers | Containers, breakbulk dry goods, vehicles, grains. |
| **`80` to `89**` | Tankers | Crude oil, refined petroleum, liquefied natural gas (LNG), chemicals. |
| **Other codes** | Support/Passenger | Tugboats (`52`), Fishing (`30`), Passenger Liners (`60`). |

### B. Determining Directional Flow (Arrival vs. Departure)

Direction is calculated through temporal sequencing of geo-coordinates against port boundaries:

* **Arrival:** The vessel enters your outer bounding tracking threshold moving fast $\rightarrow$ transitions to your internal localized dock polygon $\rightarrow$ drops speed below 1.5 knots $\rightarrow$ updates `nav_status` code to `1` (At anchor) or `5` (Moored).
* **Departure:** The vessel shifts from a static speed $\rightarrow$ registers acceleration trends above 4.0 knots $\rightarrow$ moves out past your narrow port geometric threshold toward international water routes.

### C. Hourly Analytical SQL Window Queries

Run these queries via an external clock scheduler (such as an hourly Airflow task or Cron runner) to compile high-performance analytics.

#### Query 1: Port Arrivals (Executed hourly)

Calculates vessels that arrived at a port or anchor position within the last hour, capturing their type, origin indicator (`destination`), and draft load profile:

```sql
SELECT 
    p.ship_id,
    m.ship_name,
    -- Interpret ship categorization
    CASE 
        WHEN m.ship_type >= 70 AND m.ship_type <= 79 THEN 'Cargo Vessel'
        WHEN m.ship_type >= 80 AND m.ship_type <= 89 THEN 'Liquid Tanker'
        ELSE 'Other Marine Vessel'
    END AS cargo_classification,
    m.destination AS declared_destination,
    m.draught AS current_water_draught_meters,
    min(p.ts) AS arrival_timestamp
FROM vessels_tracking.vessel_positions AS p
-- Join with state tracking profile table
FINAL LEFT JOIN vessels_tracking.vessel_metadata AS m 
    ON p.ship_id = m.ship_id
WHERE p.ts >= now() - INTERVAL 1 HOUR
  AND p.speed < 1.5
  AND p.nav_status IN (1, 5) -- 1 = Anchored, 5 = Moored
GROUP BY 
    p.ship_id, 
    m.ship_name, 
    m.ship_type, 
    m.destination, 
    m.draught
ORDER BY arrival_timestamp DESC;

```

#### Query 2: Port Departures (Executed hourly)

Identifies vessels that departed static setups, accelerating outwards toward external paths:

```sql
SELECT 
    p.ship_id,
    m.ship_name,
    CASE 
        WHEN m.ship_type >= 70 AND m.ship_type <= 79 THEN 'Cargo Vessel'
        WHEN m.ship_type >= 80 AND m.ship_type <= 89 THEN 'Liquid Tanker'
        ELSE 'Other Marine Vessel'
    END AS cargo_classification,
    m.destination AS reported_next_port,
    m.draught AS departure_draught_meters,
    max(p.ts) AS departure_timestamp
FROM vessels_tracking.vessel_positions AS p
FINAL LEFT JOIN vessels_tracking.vessel_metadata AS m 
    ON p.ship_id = m.ship_id
WHERE p.ts >= now() - INTERVAL 1 HOUR
  AND p.speed >= 4.5
  AND p.nav_status = 0 -- 0 = Under way using engine
GROUP BY 
    p.ship_id, 
    m.ship_name, 
    m.ship_type, 
    m.destination, 
    m.draught
ORDER BY departure_timestamp DESC;

```

### D. Measuring Cargo Loading/Unloading Shifts

By comparing arrival draft measurements against departure draft measurements for the same vessel over time, you can infer whether cargo was loaded or discharged:

```sql
SELECT 
    ship_id,
    m.ship_name,
    min(m.draught) AS empty_or_low_draught,
    max(m.draught) AS maximum_loaded_draught,
    abs(max(m.draught) - min(m.draught)) AS total_displacement_delta_meters
FROM vessels_tracking.vessel_metadata FINAL AS m
GROUP BY ship_id, m.ship_name
HAVING total_displacement_delta_meters > 1.0;

```

If a cargo ship arrives at 14.2 meters of draft and departs at 9.1 meters of draft, you can analytically deduce that it **discharged a massive volume of freight** at your port.