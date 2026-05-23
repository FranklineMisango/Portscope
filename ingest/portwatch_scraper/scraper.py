"""
PortWatch Scraper
=================
Pulls daily port/chokepoint data from ArcGIS Feature Services.

The IMF PortWatch pages (https://portwatch.imf.org/pages/{pageid}) are 
ArcGIS Hub pages that display data from two feature services:
  - Daily_Ports_Data:     port calls, imports, exports by vessel type
  - Daily_Chokepoints_Data: transits and capacity by vessel type

We query these services directly instead of scraping rendered HTML.
"""

import os
import sys
import json
import time
import csv
import io
import logging
from datetime import datetime, date
from typing import Optional, Dict, Any, List, Tuple
from concurrent.futures import ThreadPoolExecutor, as_completed

import requests
from tenacity import retry, stop_after_attempt, wait_exponential

logging.basicConfig(
    level=logging.INFO,
    format='%(asctime)s [%(levelname)s] %(name)s: %(message)s',
)
log = logging.getLogger('portwatch_scraper')

# =============================================================================
# Configuration
# =============================================================================

# ArcGIS Feature Service endpoints
DAILY_PORTS_SERVICE = (
    "https://services9.arcgis.com/weJ1QsnbMYJlCHdG/arcgis/rest/services/"
    "Daily_Ports_Data/FeatureServer/0/query"
)
DAILY_CHOKEPOINTS_SERVICE = (
    "https://services9.arcgis.com/weJ1QsnbMYJlCHdG/arcgis/rest/services/"
    "Daily_Chokepoints_Data/FeatureServer/0/query"
)

# Rate limiting
RATE_LIMIT_RPS = float(os.getenv("PW_SCRAPE_RATE", "5"))
MIN_DELAY = 1.0 / RATE_LIMIT_RPS

# Concurrency
MAX_WORKERS = int(os.getenv("PW_SCRAPE_WORKERS", "10"))

# Pagination - how many records per query
PAGE_SIZE = 1000

# Date range to fetch (days back from today)
DEFAULT_DAYS_BACK = int(os.getenv("PW_DAYS_BACK", "365"))

# Output
OUTPUT_DIR = os.getenv("PW_OUTPUT_DIR", "/tmp/portwatch_data")
os.makedirs(OUTPUT_DIR, exist_ok=True)


# =============================================================================
# ArcGIS Feature Service Client
# =============================================================================

class ArcGISFeatureClient:
    """Client for querying ArcGIS Feature Services with pagination."""

    def __init__(self):
        self.session = requests.Session()
        self.session.headers.update({
            "User-Agent": "Portscope-Scraper/1.0 (ArcGIS Feature Service Client)",
            "Accept": "application/json",
        })
        self._last_request_time = 0.0

    def _rate_limit(self):
        elapsed = time.time() - self._last_request_time
        if elapsed < MIN_DELAY:
            time.sleep(MIN_DELAY - elapsed)
        self._last_request_time = time.time()

    @retry(
        stop=stop_after_attempt(3),
        wait=wait_exponential(multiplier=1, min=2, max=30),
    )
    def query(self, service_url: str, where: str = "1=1",
              out_fields: str = "*", order: str = "date DESC",
              limit: int = PAGE_SIZE, offset: int = 0) -> Tuple[List[Dict], bool]:
        """
        Query an ArcGIS Feature Service.
        Returns (features, exceeded_transfer_limit).
        """
        self._rate_limit()
        params = {
            "where": where,
            "outFields": out_fields,
            "orderByFields": order,
            "returnGeometry": "false",
            "resultRecordCount": limit,
            "resultOffset": offset,
            "f": "json",
        }
        resp = self.session.get(service_url, params=params, timeout=60)
        resp.raise_for_status()
        data = resp.json()

        if "error" in data:
            raise RuntimeError(f"ArcGIS error: {data['error']}")

        features = data.get("features", [])
        exceeded = data.get("exceededTransferLimit", False)
        return features, exceeded

    def query_all(self, service_url: str, where: str = "1=1",
                  out_fields: str = "*", order: str = "date DESC",
                  max_records: int = 50000) -> List[Dict]:
        """
        Fetch ALL records from a feature service with pagination.
        """
        all_features = []
        offset = 0

        while offset < max_records:
            features, exceeded = self.query(
                service_url, where=where, out_fields=out_fields,
                order=order, offset=offset
            )
            all_features.extend(features)
            log.info(f"  Fetched {len(features)} records (offset={offset}, total={len(all_features)})")

            if not exceeded or len(features) == 0:
                break
            offset += PAGE_SIZE

        return all_features


# =============================================================================
# Data Extraction
# =============================================================================

def extract_port_data(features: List[Dict]) -> Dict[str, Any]:
    """
    Extract daily port data from feature service records.
    Returns dict keyed by portid -> {portname, country, ISO3, daily_data: [...]}
    """
    ports = {}

    for feat in features:
        a = feat["attributes"]

        # Handle date fields (could be string '2024-01-15' or timestamp)
        raw_date = a.get("date", "")
        date_str = raw_date if isinstance(raw_date, str) else str(raw_date)[:10]

        # Map field names to clean keys
        record = {
            "date": date_str,
            "year": a.get("year"),
            "month": a.get("month"),
            "day": a.get("day"),
            "portcalls": {
                "total": a.get("portcalls", 0) or 0,
                "container": a.get("portcalls_container", 0) or 0,
                "dry_bulk": a.get("portcalls_dry_bulk", 0) or 0,
                "general_cargo": a.get("portcalls_general_cargo", 0) or 0,
                "roro": a.get("portcalls_roro", 0) or 0,
                "tanker": a.get("portcalls_tanker", 0) or 0,
                "cargo": a.get("portcalls_cargo", 0) or 0,
            },
            "imports": {
                "total": a.get("import", 0) or 0,
                "container": a.get("import_container", 0) or 0,
                "dry_bulk": a.get("import_dry_bulk", 0) or 0,
                "general_cargo": a.get("import_general_cargo", 0) or 0,
                "roro": a.get("import_roro", 0) or 0,
                "tanker": a.get("import_tanker", 0) or 0,
                "cargo": a.get("import_cargo", 0) or 0,
            },
            "exports": {
                "total": a.get("export", 0) or 0,
                "container": a.get("export_container", 0) or 0,
                "dry_bulk": a.get("export_dry_bulk", 0) or 0,
                "general_cargo": a.get("export_general_cargo", 0) or 0,
                "roro": a.get("export_roro", 0) or 0,
                "tanker": a.get("export_tanker", 0) or 0,
                "cargo": a.get("export_cargo", 0) or 0,
            },
        }

        portid = a.get("portid", "")
        if not portid:
            continue

        if portid not in ports:
            ports[portid] = {
                "portid": portid,
                "portname": a.get("portname", ""),
                "country": a.get("country", ""),
                "iso3": a.get("ISO3", ""),
                "daily_data": [],
            }
        ports[portid]["daily_data"].append(record)

    return ports


def extract_chokepoint_data(features: List[Dict]) -> Dict[str, Any]:
    """Extract daily chokepoint data from feature service records."""
    chokepoints = {}

    for feat in features:
        a = feat["attributes"]
        raw_date = a.get("date", "")
        date_str = raw_date if isinstance(raw_date, str) else str(raw_date)[:10]

        record = {
            "date": date_str,
            "year": a.get("year"),
            "month": a.get("month"),
            "day": a.get("day"),
            "transits": {
                "total": a.get("n_total", 0) or 0,
                "container": a.get("n_container", 0) or 0,
                "dry_bulk": a.get("n_dry_bulk", 0) or 0,
                "general_cargo": a.get("n_general_cargo", 0) or 0,
                "roro": a.get("n_roro", 0) or 0,
                "tanker": a.get("n_tanker", 0) or 0,
                "cargo": a.get("n_cargo", 0) or 0,
            },
            "capacity": {
                "total": a.get("capacity", 0) or 0,
                "container": a.get("capacity_container", 0) or 0,
                "dry_bulk": a.get("capacity_dry_bulk", 0) or 0,
                "general_cargo": a.get("capacity_general_cargo", 0) or 0,
                "roro": a.get("capacity_roro", 0) or 0,
                "tanker": a.get("capacity_tanker", 0) or 0,
                "cargo": a.get("capacity_cargo", 0) or 0,
            },
        }

        portid = a.get("portid", "")
        if not portid:
            continue

        if portid not in chokepoints:
            chokepoints[portid] = {
                "portid": portid,
                "portname": a.get("portname", ""),
                "daily_data": [],
            }
        chokepoints[portid]["daily_data"].append(record)

    return chokepoints


# =============================================================================
# Aggregation & Time Series
# =============================================================================

def build_monthly_time_series(daily_records: List[Dict]) -> Dict[str, Any]:
    """Aggregate daily records into monthly time series for charts."""
    from collections import defaultdict

    monthly = defaultdict(lambda: {
        "portcalls": 0,
        "imports": 0,
        "exports": 0,
        "days": 0,
    })

    for record in daily_records:
        d = record.get("date", "")
        month_key = d[:7]  # "2024-01"
        m = monthly[month_key]
        m["portcalls"] += record.get("portcalls", {}).get("total", 0)
        m["imports"] += record.get("imports", {}).get("total", 0)
        m["exports"] += record.get("exports", {}).get("total", 0)
        m["days"] += 1

    labels = sorted(monthly.keys())
    return {
        "labels": labels,
        "portcalls": [monthly[l]["portcalls"] for l in labels],
        "imports": [monthly[l]["imports"] for l in labels],
        "exports": [monthly[l]["exports"] for l in labels],
        "summary": {
            "total_portcalls": sum(monthly[l]["portcalls"] for l in labels),
            "total_imports": sum(monthly[l]["imports"] for l in labels),
            "total_exports": sum(monthly[l]["exports"] for l in labels),
            "avg_daily_portcalls": round(
                sum(monthly[l]["portcalls"] for l in labels) / max(sum(monthly[l]["days"] for l in labels), 1), 1
            ),
        },
    }


# =============================================================================
# Storage & Export
# =============================================================================

def export_to_json(data: Any, filename: str):
    """Export data to JSON file."""
    path = os.path.join(OUTPUT_DIR, filename)
    with open(path, "w") as f:
        json.dump(data, f, indent=2, default=str)
    log.info(f"Exported to {path}")
    return path


def export_to_csv(records: List[Dict], filename: str, flat: bool = True):
    """Export records to CSV."""
    if not records:
        log.warning(f"No records to export for {filename}")
        return None

    path = os.path.join(OUTPUT_DIR, filename)
    with open(path, "w", newline="") as f:
        writer = csv.DictWriter(f, fieldnames=records[0].keys())
        writer.writeheader()
        writer.writerows(records)
    log.info(f"Exported {len(records)} rows to {path}")
    return path


# =============================================================================
# Main Scrape Pipeline
# =============================================================================

def scrape_daily_ports(ids_filter: Optional[List[str]] = None) -> Dict[str, Any]:
    """Fetch all daily port data from ArcGIS."""
    client = ArcGISFeatureClient()
    log.info("Fetching Daily_Ports_Data...")

    where = "1=1"
    if ids_filter:
        ids_str = ", ".join(f"'{i}'" for i in ids_filter)
        where = f"portid IN ({ids_str})"

    features = client.query_all(DAILY_PORTS_SERVICE, where=where)
    log.info(f"  Total records: {len(features)}")

    ports = extract_port_data(features)
    log.info(f"  Extracted {len(ports)} ports")

    return ports


def scrape_daily_chokepoints(ids_filter: Optional[List[str]] = None) -> Dict[str, Any]:
    """Fetch all daily chokepoint data from ArcGIS."""
    client = ArcGISFeatureClient()
    log.info("Fetching Daily_Chokepoints_Data...")

    where = "1=1"
    if ids_filter:
        ids_str = ", ".join(f"'{i}'" for i in ids_filter)
        where = f"portid IN ({ids_str})"

    features = client.query_all(DAILY_CHOKEPOINTS_SERVICE, where=where)
    log.info(f"  Total records: {len(features)}")

    chokepoints = extract_chokepoint_data(features)
    log.info(f"  Extracted {len(chokepoints)} chokepoints")

    return chokepoints


def build_lookup_index(ports_data: Dict, chokepoints_data: Dict) -> Dict[str, Any]:
    """
    Build a pageid -> data lookup so frontend can query by pageid.
    
    The pageid in our CSV maps to a 'portid' in the feature service.
    e.g. pageid='3b1f40eb...' maps to portid='port1325'
    """
    # Load the portid -> pageid mapping from CSV
    import pandas as pd
    mapping = {}

    try:
        ports_csv = os.getenv("PW_PORTS_CSV", "/app/data/Ports.csv")
        df = pd.read_csv(ports_csv)
        for _, row in df.iterrows():
            pid = row.get("portid", "")
            pgid = row.get("pageid", "")
            if pid and pgid:
                mapping[str(pgid)] = {
                    "pageid": str(pgid),
                    "portid": str(pid),
                    "portname": str(row.get("fullname", row.get("portname", ""))),
                    "country": str(row.get("country", "")),
                    "iso3": str(row.get("ISO3", "")),
                    "lat": float(row.get("lat", 0)),
                    "lon": float(row.get("lon", 0)),
                }
    except Exception as e:
        log.warning(f"Could not load port mapping: {e}")

    # Also load chokepoint mapping
    try:
        cp_csv = os.getenv("PW_CHOKEPOINTS_CSV", "/app/data/PortWatch_chokepoints_database.csv")
        df = pd.read_csv(cp_csv)
        for _, row in df.iterrows():
            pid = row.get("portid", "")
            pgid = row.get("pageid", "")
            if pid and pgid:
                mapping[str(pgid)] = {
                    "pageid": str(pgid),
                    "portid": str(pid),
                    "portname": str(row.get("fullname", row.get("portname", ""))),
                    "country": str(row.get("country", "")),
                    "iso3": str(row.get("ISO3", "")),
                    "lat": float(row.get("lat", 0)),
                    "lon": float(row.get("lon", 0)),
                }
    except Exception as e:
        log.warning(f"Could not load chokepoint mapping: {e}")

    # Attach daily data to mapping
    for pgid, info in mapping.items():
        portid = info["portid"]
        if portid in ports_data:
            info["daily_data"] = ports_data[portid].get("daily_data", [])
            info["monthly"] = build_monthly_time_series(info.get("daily_data", []))
            info["port_type"] = "port"
        elif portid in chokepoints_data:
            info["daily_data"] = chokepoints_data[portid].get("daily_data", [])
            info["monthly"] = build_monthly_time_series(info.get("daily_data", []))
            info["port_type"] = "chokepoint"

    log.info(f"Built lookup index with {len(mapping)} entries")
    return mapping


# =============================================================================
# CLI Entry Point
# =============================================================================

def main():
    import argparse
    parser = argparse.ArgumentParser(description="PortWatch Scraper (ArcGIS Feature Service)")
    parser.add_argument("--mode", choices=["ports", "chokepoints", "all", "single"],
                        default="all", help="What to scrape")
    parser.add_argument("--portid", help="Single portid to scrape (e.g. port19)")
    parser.add_argument("--export", choices=["json", "csv", "both"], default="json",
                        help="Export format")
    args = parser.parse_args()

    ids_filter = [args.portid] if args.portid and args.mode == "single" else None

    ports_data = {}
    chokepoints_data = {}

    if args.mode in ("ports", "all"):
        ports_data = scrape_daily_ports(ids_filter)
        export_to_json(ports_data, "daily_ports.json")

    if args.mode in ("chokepoints", "all"):
        chokepoints_data = scrape_daily_chokepoints(ids_filter)
        export_to_json(chokepoints_data, "daily_chokepoints.json")

    if args.mode == "all" or (ports_data and chokepoints_data):
        lookup = build_lookup_index(ports_data, chokepoints_data)
        export_to_json(lookup, "portwatch_lookup.json")

        # Summary
        has_data = sum(1 for v in lookup.values() if v.get("daily_data"))
        total_daily = sum(len(v.get("daily_data", [])) for v in lookup.values())
        log.info(f"=== SUMMARY ===")
        log.info(f"  Ports with daily data: {has_data} / {len(lookup)}")
        log.info(f"  Total daily records: {total_daily}")
        log.info(f"  Output directory: {OUTPUT_DIR}")

        # Print sample
        for pgid, info in list(lookup.items())[:2]:
            dd = info.get("daily_data", [])
            print(f"\n  {info['portname']:30s} (pageid={pgid})")
            print(f"  Daily records: {len(dd)}")
            if dd:
                print(f"  Latest: {dd[0]['date']} calls={dd[0]['portcalls']['total']} imports={dd[0]['imports']['total']} exports={dd[0]['exports']['total']}")
            monthly = info.get("monthly", {})
            if monthly.get("labels"):
                print(f"  Monthly range: {monthly['labels'][0]} to {monthly['labels'][-1]}")
                print(f"  Total portcalls: {monthly['summary']['total_portcalls']}")
                print(f"  Total imports: {monthly['summary']['total_imports']}")
                print(f"  Total exports: {monthly['summary']['total_exports']}")


if __name__ == "__main__":
    main()
