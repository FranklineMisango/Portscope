#!/usr/bin/env bash
docker compose down --volumes --remove-orphans
docker system prune -a --volumes
docker compose build --no-cache
docker compose up --force-recreate
docker compose -f ingest/docker-compose.yml build frontend && docker compose -f ingest/docker-compose.yml up -d --no-deps --force-recreate frontend
