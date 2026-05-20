API deployment and secrets

This API uses `POSTGRES_DSN` for database connectivity and supports simple API key auth via the `API_KEY` environment variable.

For production, do NOT set `API_KEY` in plain text. Use Vault and GitHub Actions to inject secrets during CI/CD. See `vault/policy.hcl` and `.github/workflows/ci-deploy.yml` for examples.
