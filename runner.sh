#!/usr/bin/env bash
docker compose -f ingest/docker-compose.yml build --no-cache
docker compose -f ingest/docker-compose.yml up --force-recreate
