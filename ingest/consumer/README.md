Redis Streams consumer (enhanced)

New features:
- Periodically claims pending messages idle longer than `CLAIM_IDLE_MS` and re-processes them.
- Dead-letter stream `ais:dlq` when `MAX_DELIVERIES` is exceeded.
- Reverse-geocodes positions via Nominatim and upserts into `ports` table.
- Prometheus metrics available at `/metrics` (default :9090) and health at `/health`.

Run locally (after starting services and applying migrations):

```bash
# apply migrations
psql "postgres://postgres:postgres@localhost:5432/postgres" -c "\i migrations/003_create_ports_table.sql"

# run consumer
export REDIS_ADDR=localhost:6379
export POSTGRES_DSN=postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable
export METRICS_ADDR=:9090
export MAX_DELIVERIES=5
export CLAIM_IDLE_MS=60000
cd consumer
go run main.go
```

Notes:
- This is a pragmatic enrichment step; for production use consider caching reverse geocode results and respecting Nominatim usage policy or use a paid geocoding provider.
- Pending message reclaiming and DLQ logic are basic; improvements include per-message backoff, dead-letter metadata, and idempotent processing.
