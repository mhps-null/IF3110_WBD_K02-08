#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
COMPOSE_FILE="$SCRIPT_DIR/docker-compose.yml"

echo "=== Menjalankan Container Database Testing IF3110 ==="
chmod +x "$SCRIPT_DIR/postgres-init/01-create-databases.sh" || true

docker compose -f "$COMPOSE_FILE" up -d

echo "Menunggu kesiapan PostgreSQL (5433), MySQL (3307), dan MariaDB (3308)..."

# Tunggu PostgreSQL sehat
until docker compose -f "$COMPOSE_FILE" exec -T test-postgres pg_isready -U test_user -d test_ecommerce_pg >/dev/null 2>&1; do
    printf '.'
    sleep 1
done
echo " [OK] PostgreSQL Test DB Siap (Port 5433)!"

# Tunggu MySQL sehat
until docker compose -f "$COMPOSE_FILE" exec -T test-mysql mysqladmin ping -h localhost -u test_user -ptest_password >/dev/null 2>&1; do
    printf '.'
    sleep 1
done
echo " [OK] MySQL Test DB Siap (Port 3307)!"

# Tunggu MariaDB sehat
until docker compose -f "$COMPOSE_FILE" exec -T test-mariadb mariadb-admin ping -h localhost -u test_user -ptest_password >/dev/null 2>&1; do
    printf '.'
    sleep 1
done
echo " [OK] MariaDB Test DB Siap (Port 3308)!"

echo ""
echo "=========================================================================="
echo " DATABASE TESTING BERHASIL DIJALANKAN!"
echo "=========================================================================="
echo " 1. PostgreSQL Source (test_ecommerce_pg):"
echo "    - Engine   : PostgreSQL"
echo "    - Host     : localhost"
echo "    - Port     : 5433"
echo "    - Database : test_ecommerce_pg"
echo "    - Username : test_user"
echo "    - Password : test_password"
echo "    - SSL Mode : disable"
echo ""
echo " 2. PostgreSQL Target (test_ecommerce_pg_target) [Untuk Schema Diff]:"
echo "    - Engine   : PostgreSQL"
echo "    - Host     : localhost"
echo "    - Port     : 5433"
echo "    - Database : test_ecommerce_pg_target"
echo "    - Username : test_user"
echo "    - Password : test_password"
echo "    - SSL Mode : disable"
echo ""
echo " 3. MySQL Inventory (test_inventory_mysql) [Bonus Multi-DBMS]:"
echo "    - Engine   : MySQL"
echo "    - Host     : localhost"
echo "    - Port     : 3307"
echo "    - Database : test_inventory_mysql"
echo "    - Username : test_user"
echo "    - Password : test_password"
echo "    - SSL Mode : disable"
echo ""
echo " 4. MariaDB Human Resources (test_hr_mariadb) [Bonus Multi-DBMS]:"
echo "    - Engine   : MariaDB"
echo "    - Host     : localhost"
echo "    - Port     : 3308"
echo "    - Database : test_hr_mariadb"
echo "    - Username : test_user"
echo "    - Password : test_password"
echo "    - SSL Mode : disable"
echo "=========================================================================="
echo " Tip: Jalankan './docker/register-connections.sh' untuk otomatis mendaftarkan"
echo "      seluruh koneksi ini ke Platform Admin web!"
echo "=========================================================================="
