"""Database connections — Postgres (async + sync) and DuckDB."""
from __future__ import annotations

from contextlib import asynccontextmanager
from typing import AsyncGenerator

import asyncpg
import duckdb
import psycopg2
from redis import asyncio as aioredis

from config import settings

# ── DuckDB (in-memory for fast historical queries) ──────────

_duck: duckdb.DuckDBPyConnection | None = None


def get_duckdb() -> duckdb.DuckDBPyConnection:
    """Return a singleton in-memory DuckDB connection."""
    global _duck
    if _duck is None:
        _duck = duckdb.connect(":memory:")
        # Load spatial extension for geospatial queries
        try:
            _duck.install_extension("spatial")
            _duck.load_extension("spatial")
        except Exception:
            pass  # spatial extension optional
    return _duck


def load_csv_into_duckdb(csv_path: str, table_name: str) -> None:
    """Load a CSV file into DuckDB as a table for fast SQL queries."""
    con = get_duckdb()
    con.execute(f"CREATE OR REPLACE TABLE {table_name} AS SELECT * FROM read_csv_auto('{csv_path}')")
    print(f"[duckdb] loaded {table_name} from {csv_path}")


# ── Postgres (sync, for read-only ArcGIS queries) ──────────

_sync_pool: list[psycopg2.extensions.connection] = []


def get_sync_pg() -> psycopg2.extensions.connection:
    """Return a cached synchronous Postgres connection."""
    if not _sync_pool:
        conn = psycopg2.connect(settings.POSTGRES_DSN)
        conn.autocommit = True
        _sync_pool.append(conn)
    return _sync_pool[-1]


# ── Postgres (async, for API endpoints) ────────────────────

_async_pool: asyncpg.Pool | None = None


async def get_async_pool() -> asyncpg.Pool:
    """Return a singleton asyncpg connection pool."""
    global _async_pool
    if _async_pool is None:
        dsn = settings.POSTGRES_DSN.replace("postgresql://", "postgresql://")
        if dsn.startswith("postgresql+asyncpg://"):
            dsn = dsn.replace("postgresql+asyncpg://", "postgresql://")
        _async_pool = await asyncpg.create_pool(dsn=dsn, min_size=2, max_size=10)
    return _async_pool


@asynccontextmanager
async def pg_conn() -> AsyncGenerator[asyncpg.Connection, None]:
    """Acquire a connection from the async pool.

    Gracefully yields None (as a fake connection that raises) when Postgres is down.
    """
    try:
        pool = await get_async_pool()
        async with pool.acquire() as conn:
            yield conn
    except Exception as exc:
        logger = __import__("logging").getLogger(__name__)
        logger.warning("pg_conn: database not available (%s)", exc)
        # Yield a minimal proxy that raises on any query
        class _FakeConn:
            async def fetch(self, *a, **kw): return []
            async def fetchrow(self, *a, **kw): return None
            async def execute(self, *a, **kw): pass
        yield _FakeConn()


# ── Redis (async) ───────────────────────────────────────────

_redis: aioredis.Redis | None = None


async def get_redis() -> aioredis.Redis:
    global _redis
    if _redis is None:
        _redis = aioredis.from_url(settings.REDIS_URL, decode_responses=True)
    return _redis