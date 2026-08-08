#!/usr/bin/env bash
set -euo pipefail

project_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
env_file="$project_dir/.env"
db_role="temu_app"
db_name="temu_manager"

if [[ ! -f "$env_file" ]]; then
  install -m 600 /dev/null "$env_file"
fi
chmod 600 "$env_file"

if ! grep -q '^TEMU_DATABASE_URL=' "$env_file"; then
  db_password="$(openssl rand -hex 24)"
  if sudo -u postgres psql -Atqc "SELECT 1 FROM pg_roles WHERE rolname='$db_role'" | grep -qx 1; then
    printf "ALTER ROLE %s PASSWORD '%s';\n" "$db_role" "$db_password" | sudo -u postgres psql -v ON_ERROR_STOP=1 >/dev/null
  else
    printf "CREATE ROLE %s LOGIN PASSWORD '%s';\n" "$db_role" "$db_password" | sudo -u postgres psql -v ON_ERROR_STOP=1 >/dev/null
  fi
  if ! sudo -u postgres psql -Atqc "SELECT 1 FROM pg_database WHERE datname='$db_name'" | grep -qx 1; then
    sudo -u postgres createdb --owner="$db_role" "$db_name"
  fi
  printf '\nTEMU_DATABASE_URL=postgresql://%s:%s@127.0.0.1:5432/%s\n' "$db_role" "$db_password" "$db_name" >> "$env_file"
fi

if ! grep -q '^TEMU_OPERATION_KEY=' "$env_file"; then
  printf 'TEMU_OPERATION_KEY=%s\n' "$(openssl rand -hex 24)" >> "$env_file"
fi

grep -q '^TEMU_LISTEN=' "$env_file" || printf 'TEMU_LISTEN=127.0.0.1:18082\n' >> "$env_file"
grep -q '^TEMU_ORDER_SYNC_INTERVAL=' "$env_file" || printf 'TEMU_ORDER_SYNC_INTERVAL=5m\n' >> "$env_file"
grep -q '^TEMU_REQUEST_TIMEOUT=' "$env_file" || printf 'TEMU_REQUEST_TIMEOUT=30s\n' >> "$env_file"
grep -q '^TEMU_SHIPPING_QUOTE_LIFETIME=' "$env_file" || printf 'TEMU_SHIPPING_QUOTE_LIFETIME=10m\n' >> "$env_file"
grep -q '^XLWMS_WAREHOUSE_DECISION_URL=' "$env_file" || printf 'XLWMS_WAREHOUSE_DECISION_URL=https://pangutech.online/warehouse-console/api/temu/warehouse-availability/query\n' >> "$env_file"

chmod 600 "$env_file"
echo "Temu PostgreSQL and private runtime variables are provisioned."
