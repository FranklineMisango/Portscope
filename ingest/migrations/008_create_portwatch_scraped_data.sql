-- =============================================================================
-- PortWatch Scraped Data Tables
-- =============================================================================
-- These tables store the full scraped PortWatch data in structured + JSONB format.
-- The normalized_metrics table is what the frontend sidebar queries.
-- =============================================================================

-- 1. PortWatch scrape jobs (tracking)
CREATE TABLE IF NOT EXISTS portwatch_scrape_jobs (
  id BIGSERIAL PRIMARY KEY,
  started_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  completed_at TIMESTAMPTZ,
  total_pages INT NOT NULL DEFAULT 0,
  successful_pages INT NOT NULL DEFAULT 0,
  failed_pages INT NOT NULL DEFAULT 0,
  status TEXT NOT NULL DEFAULT 'running'
    CHECK (status IN ('running', 'completed', 'failed')),
  error TEXT
);

CREATE INDEX IF NOT EXISTS idx_portwatch_scrape_jobs_status
  ON portwatch_scrape_jobs (status, started_at DESC);

-- 2. Raw scraped pages (complete JSONB payload from scraper)
CREATE TABLE IF NOT EXISTS portwatch_scraped_pages (
  id BIGSERIAL PRIMARY KEY,
  pageid TEXT NOT NULL UNIQUE,
  job_id BIGINT REFERENCES portwatch_scrape_jobs(id) ON DELETE SET NULL,
  port_name TEXT,
  port_type TEXT CHECK (port_type IN ('port', 'chokepoint')),
  country TEXT,
  iso3 TEXT,
  coordinates JSONB,         -- [lon, lat]
  raw_payload JSONB NOT NULL, -- Complete scraper output
  scraped_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  success BOOLEAN NOT NULL DEFAULT false,
  error TEXT
);

CREATE INDEX IF NOT EXISTS idx_portwatch_scraped_pages_pageid
  ON portwatch_scraped_pages (pageid);
CREATE INDEX IF NOT EXISTS idx_portwatch_scraped_pages_type
  ON portwatch_scraped_pages (port_type);
CREATE INDEX IF NOT EXISTS idx_portwatch_scraped_pages_name
  ON portwatch_scraped_pages (port_name);

-- Helper: Enable trgm extension if not already (must be before gin_trgm_ops index)
CREATE EXTENSION IF NOT EXISTS pg_trgm;

-- 3. Normalized metrics (main query target for sidebar)
CREATE TABLE IF NOT EXISTS portwatch_normalized_metrics (
  id BIGSERIAL PRIMARY KEY,
  pageid TEXT NOT NULL UNIQUE REFERENCES portwatch_scraped_pages(pageid) ON DELETE CASCADE,
  
  -- Metadata
  port_name TEXT NOT NULL,
  port_type TEXT NOT NULL DEFAULT 'port',
  country TEXT,
  iso3 TEXT,
  
  -- Vessel metrics (structured)
  total_vessels INT NOT NULL DEFAULT 0,
  vessel_breakdown JSONB NOT NULL DEFAULT '{}'::jsonb,
  primary_vessel_type TEXT,
  vessel_diversity NUMERIC(5,4) DEFAULT 0,
  
  -- Industry metrics
  top_industries JSONB NOT NULL DEFAULT '[]'::jsonb,
  primary_industry TEXT,
  
  -- Time series data from CSVs (stored as JSONB for flexibility)
  timeseries_data JSONB NOT NULL DEFAULT '{}'::jsonb,
  
  -- Aggregate/summary metrics
  aggregates JSONB NOT NULL DEFAULT '{}'::jsonb,
  
  -- Full reference to raw scraped data
  raw_metrics JSONB NOT NULL DEFAULT '{}'::jsonb,
  
  -- Metadata
  scraped_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_normalized_metrics_pageid
  ON portwatch_normalized_metrics (pageid);
CREATE INDEX IF NOT EXISTS idx_normalized_metrics_type
  ON portwatch_normalized_metrics (port_type);
CREATE INDEX IF NOT EXISTS idx_normalized_metrics_country
  ON portwatch_normalized_metrics (country);
CREATE INDEX IF NOT EXISTS idx_normalized_metrics_total_vessels
  ON portwatch_normalized_metrics (total_vessels DESC);
CREATE INDEX IF NOT EXISTS idx_normalized_metrics_name_trgm
  ON portwatch_normalized_metrics USING GIN (port_name gin_trgm_ops);

-- 4. Time-series data (for efficient chart queries)
CREATE TABLE IF NOT EXISTS portwatch_timeseries (
  id BIGSERIAL PRIMARY KEY,
  pageid TEXT NOT NULL REFERENCES portwatch_scraped_pages(pageid) ON DELETE CASCADE,
  dataset_name TEXT NOT NULL,  -- e.g., 'vessel_traffic_monthly', 'trade_volume'
  date_label TEXT NOT NULL,    -- e.g., '2024-01'
  value NUMERIC NOT NULL DEFAULT 0,
  UNIQUE (pageid, dataset_name, date_label)
);

CREATE INDEX IF NOT EXISTS idx_portwatch_timeseries_pageid
  ON portwatch_timeseries (pageid, dataset_name, date_label);

-- Helper: Update updated_at trigger
CREATE OR REPLACE FUNCTION update_modified_column()
RETURNS TRIGGER AS $$
BEGIN
  NEW.updated_at = NOW();
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_portwatch_normalized_metrics_updated
  ON portwatch_normalized_metrics;
CREATE TRIGGER trg_portwatch_normalized_metrics_updated
  BEFORE UPDATE ON portwatch_normalized_metrics
  FOR EACH ROW EXECUTE FUNCTION update_modified_column();
