#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
COMPOSE_FILE="$SCRIPT_DIR/docker-compose.yml"

echo "=== Menghentikan Container Database Testing IF3110 ==="
docker compose -f "$COMPOSE_FILE" down

echo "Container berhasil dihentikan."
