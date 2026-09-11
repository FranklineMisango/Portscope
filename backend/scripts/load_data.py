#!/usr/bin/env python3
"""Load CSV data into DuckDB for fast historical queries.

Usage:
    python scripts/load_data.py

Loads data/Ports.csv and data/PortWatch_Chokepoints_database.csv into DuckDB
in-memory tables so the API can serve historical queries in milliseconds.
"""
from __future__ import annotations

import argparse
from pathlib import Path

import duckdb

DATA_DIR = Path(__file__).resolve().parent.parent.parent / "data"


def main(verbose: bool = False):
    con = duckdb.connect(":memory:")

    csv_files = [
        ("ports", DATA_DIR / "Ports.csv"),
        ("chokepoints", DATA_DIR / "PortWatch_Chokepoints_database.csv"),
    ]

    for name, path in csv_files:
        if not path.exists():
            print(f"[warn] {path} not found, skipping")
            continue
        con.execute(
            f"CREATE OR REPLACE TABLE {name} AS SELECT * FROM read_csv_auto('{path}')"
        )
        count = con.execute(f"SELECT count(*) FROM {name}").fetchone()[0]
        print(f"[duckdb] loaded {name}: {count} rows from {path.name}")

    if verbose:
        for name, _ in csv_files:
            try:
                print(f"\n--- {name} (first 3 rows) ---")
                print(con.execute(f"SELECT * FROM {name} LIMIT 3").fetchdf().to_string())
            except Exception:
                pass

    # Keep the connection alive
    print("\nDuckDB loaded. Ready for fast SQL queries.")
    return con


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--verbose", "-v", action="store_true")
    args = parser.parse_args()
    main(verbose=args.verbose)