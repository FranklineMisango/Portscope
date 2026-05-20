#!/usr/bin/env bash
# Fetch secrets from Vault and export as env vars for local testing
# Requires: vault CLI logged in (VAULT_ADDR, VAULT_TOKEN)
set -euo pipefail
SECRET_PATH=${1:-secret/data/portscope/ais}
if [ -z "$VAULT_ADDR" ]; then
  echo "VAULT_ADDR not set"
  exit 1
fi
if [ -z "$(vault token lookup -format=json 2>/dev/null || true)" ]; then
  echo "Not logged into Vault. Run 'vault login' first."
  exit 1
fi
resp=$(vault kv get -format=json "$SECRET_PATH")
if [ -z "$resp" ]; then
  echo "no secret at $SECRET_PATH"
  exit 1
fi
# print exports
echo "$resp" | jq -r '.data.data | to_entries[] | "export \\(.key | ascii_upcase)=\"\\(.value)\""'
