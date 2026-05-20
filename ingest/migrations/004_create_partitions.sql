-- Create partitioned traffic_logs table (range partitioned by month)
BEGIN;

-- if traffic_logs already exists, skip or adapt
CREATE TABLE IF NOT EXISTS traffic_logs (
    id BIGSERIAL PRIMARY KEY,
    mmsi BIGINT NOT NULL,
    speed numeric,
    course numeric,
    reported_at timestamptz NOT NULL,
    geom geometry(POINT, 4326),
    port_id INTEGER,
    created_at timestamptz DEFAULT now()
) PARTITION BY RANGE (reported_at);

-- Function to create monthly partitions
CREATE OR REPLACE FUNCTION create_traffic_partition(year int, month int)
RETURNS void AS $$
DECLARE
  partition_name text := format('traffic_logs_p%04d%02d', year, month);
  from_ts timestamptz := make_timestamptz(year, month, 1, 0, 0, 0);
  to_ts timestamptz := (from_ts + interval '1 month');
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_class WHERE relname = partition_name) THEN
    EXECUTE format('CREATE TABLE IF NOT EXISTS %I PARTITION OF traffic_logs FOR VALUES FROM (%L) TO (%L);', partition_name, from_ts, to_ts);
  END IF;
END;
$$ LANGUAGE plpgsql;

-- Create the current month partition
SELECT create_traffic_partition(EXTRACT(YEAR FROM now())::int, EXTRACT(MONTH FROM now())::int);

COMMIT;
