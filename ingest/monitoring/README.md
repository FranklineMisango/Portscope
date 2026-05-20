Prometheus monitoring

This adds a Prometheus server to the `ingest` docker-compose so you can scrape the consumer's `/metrics` endpoint.

Start Prometheus with the other services:

```bash
cd ingest
docker compose up -d
```

Prometheus UI: http://localhost:9090

Notes:
- The `prometheus.yml` scrapes `consumer:9090` by default. To allow Prometheus to scrape the consumer, either:
  - Run the consumer inside the same compose network (add a `consumer` service that builds `./consumer`).
  - Or change `prometheus.yml` to point at your host IP (e.g. `host.docker.internal:9090` on some platforms) or use `network_mode: host`.
- Add `redis_exporter` and `postgres_exporter` for deeper metrics.
The compose now includes exporters for Redis and Postgres. `prometheus.yml` is preconfigured to scrape:

- `consumer:9090` (if consumer runs in compose)
- `redis_exporter:9121`
- `postgres_exporter:9187`

To run everything locally:

```bash
cd ingest
docker compose up -d
```

Prometheus UI: http://localhost:9090

Notes:
- If you prefer to run the Go `consumer` locally (not in compose), update `prometheus.yml` to point at your host endpoint or run the consumer as a compose service.
- For production, add secure credentials and monitoring exporters behind appropriate network controls.
