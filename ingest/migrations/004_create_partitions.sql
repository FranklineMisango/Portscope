-- Add partition support to existing traffic_logs table
-- This migration is safe to re-run if the table already exists

-- Create monthly partitions for traffic_logs using table inheritance
-- This is compatible with the existing traffic_logs table from migration 002
CREATE OR REPLACE FUNCTION create_traffic_partition(year int, month int)
RETURNS void AS $$
DECLARE
  partition_name text := 'traffic_logs_p' || year::text || LPAD(month::text, 2, '0');
  from_ts timestamptz := make_timestamptz(year, month, 1, 0, 0, 0);
  to_ts timestamptz := (from_ts + interval '1 month');
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_class WHERE relname = partition_name) THEN
    EXECUTE format('
      CREATE TABLE %I (
        CHECK (event_time >= %L AND event_time < %L)
      ) INHERITS (traffic_logs);
      CREATE INDEX ON %I (event_time);
      CREATE INDEX ON %I (mmsi);
    ', partition_name, from_ts, to_ts, partition_name, partition_name);
  END IF;
END;
$$ LANGUAGE plpgsql;

-- Create the current month partition
SELECT create_traffic_partition(EXTRACT(YEAR FROM now())::int, EXTRACT(MONTH FROM now())::int);

