"""Application configuration loaded from environment variables."""
from __future__ import annotations

import os
from pathlib import Path
from typing import Literal

from dotenv import load_dotenv

load_dotenv(Path(__file__).resolve().parent.parent / ".env")


class Settings:
    # ── Postgres ──────────────────────────────────────────────
    POSTGRES_DSN: str = os.getenv(
        "POSTGRES_DSN",
        "postgresql://postgres:postgres@localhost:5432/postgres",
    )
    POSTGRES_DSN_ASYNC: str = os.getenv(
        "POSTGRES_DSN_ASYNC",
        "postgresql+asyncpg://postgres:postgres@localhost:5432/postgres",
    )

    # ── Redis ─────────────────────────────────────────────────
    REDIS_URL: str = os.getenv("REDIS_URL", "redis://localhost:6379/0")

    # ── AISStream.io ─────────────────────────────────────────
    AISSTREAM_API_KEY: str = os.getenv("AISSTREAM_API_KEY", "")

    # ── ArcGIS FeatureServer endpoints ───────────────────────
    ARCGIS_PORTS_URL: str = os.getenv(
        "ARCGIS_PORTS_URL",
        "https://services9.arcgis.com/weJ1QsnbMYJlCHdG/arcgis/rest/services/"
        "PortWatch_ports_database/FeatureServer/0/query",
    )
    ARCGIS_CHOKEPOINTS_URL: str = os.getenv(
        "ARCGIS_CHOKEPOINTS_URL",
        "https://services9.arcgis.com/weJ1QsnbMYJlCHdG/arcgis/rest/services/"
        "PortWatch_chokepoints_database/FeatureServer/0/query",
    )

    # ── ML model path ────────────────────────────────────────
    MODEL_PATH: str = os.getenv("MODEL_PATH", "models/portscope_lgbm.txt")

    # ── Server ───────────────────────────────────────────────
    HOST: str = os.getenv("API_HOST", "0.0.0.0")
    PORT: int = int(os.getenv("API_PORT", "8000"))
    DEBUG: bool = os.getenv("API_DEBUG", "false").lower() == "true"
    CORS_ORIGINS: list[str] = os.getenv(
        "CORS_ORIGINS", "http://localhost:5173,http://localhost:3000"
    ).split(",")

    # ── Feature toggles ──────────────────────────────────────
    ENABLE_AIS_STREAMING: bool = (
        os.getenv("ENABLE_AIS_STREAMING", "true").lower() == "true"
    )
    ENABLE_MODEL: bool = os.getenv("ENABLE_MODEL", "false").lower() == "true"


settings = Settings()