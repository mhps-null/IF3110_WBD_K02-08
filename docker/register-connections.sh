#!/usr/bin/env bash
# ==============================================================================
# SCRIPT PENDAFTARAN KONEKSI DATABASE DOCKER KE PLATFORM ADMIN (MILESTONE 2 JSON API)
# ==============================================================================
set -euo pipefail

BASE_URL="${BASE_URL:-http://localhost:8080}"
ADMIN_USER="${ADMIN_USER:-admin}"
ADMIN_PASS="${ADMIN_PASS:-1234567891011121314}"

TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT
COOKIE_JAR="$TMP_DIR/cookies.txt"

echo "=== Mendaftarkan Koneksi Database Testing ke Platform Web (JSON API) ==="

# 1. Ambil CSRF token dari GET /api/auth/csrf
CSRF_TOKEN="$(curl -s -c "$COOKIE_JAR" "$BASE_URL/api/auth/csrf" | sed -n 's/.*"csrf_token"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p')"
if [[ -z "$CSRF_TOKEN" ]]; then
    echo "ERROR: Gagal mengambil CSRF token dari $BASE_URL/api/auth/csrf" >&2
    exit 1
fi

# 2. Login sebagai Platform Admin melalui POST /api/auth/login
LOGIN_Payload="$(printf '{"username":"%s","password":"%s"}' "$ADMIN_USER" "$ADMIN_PASS")"
LOGIN_STATUS="$(curl -s -b "$COOKIE_JAR" -c "$COOKIE_JAR" \
    -H "Content-Type: application/json" \
    -H "X-CSRF-Token: $CSRF_TOKEN" \
    -d "$LOGIN_Payload" \
    -w "%{http_code}" -o /dev/null "$BASE_URL/api/auth/login")"

if [[ "$LOGIN_STATUS" != "200" ]]; then
    echo "ERROR: Login gagal (HTTP $LOGIN_STATUS). Pastikan kredensial ADMIN_USER/ADMIN_PASS sesuai." >&2
    exit 1
fi
echo " [OK] Berhasil login sebagai Platform Admin ($ADMIN_USER)"

# Helper function to register connection via POST /api/admin/connections
register_db() {
    local name="$1"
    local engine="$2"
    local host="$3"
    local port="$4"
    local db_name="$5"
    local user="$6"
    local pass="$7"

    local payload
    payload="$(printf '{"name":"%s","engine":"%s","host":"%s","port":%s,"database_name":"%s","username":"%s","password":"%s","ssl_mode":"disable"}' \
        "$name" "$engine" "$host" "$port" "$db_name" "$user" "$pass")"

    local status
    status="$(curl -s -b "$COOKIE_JAR" -c "$COOKIE_JAR" \
        -H "Content-Type: application/json" \
        -H "X-CSRF-Token: $CSRF_TOKEN" \
        -d "$payload" \
        -w "%{http_code}" -o /dev/null "$BASE_URL/api/admin/connections")"

    echo " [OK] Profil koneksi '$name' berhasil didaftarkan (HTTP $status)"
}

# 3. Daftarkan PostgreSQL E-Commerce (Source + Dataset 500k Row)
register_db "Docker PostgreSQL E-Commerce (Source)" "postgres" "localhost" "5433" "test_ecommerce_pg" "test_user" "test_password"

# 4. Daftarkan PostgreSQL E-Commerce Target (Untuk Schema Diff)
register_db "Docker PostgreSQL E-Commerce (Target Diff)" "postgres" "localhost" "5433" "test_ecommerce_pg_target" "test_user" "test_password"

# 5. Daftarkan MySQL Inventory (Bonus Multi-DBMS M1)
register_db "Docker MySQL Inventory" "mysql" "localhost" "3307" "test_inventory_mysql" "test_user" "test_password"

# 6. Daftarkan MariaDB Human Resources (Bonus Multi-DBMS M1)
register_db "Docker MariaDB Human Resources" "mariadb" "localhost" "3308" "test_hr_mariadb" "test_user" "test_password"

# 7. Berikan Hak Akses View + Edit (Level 2) ke seluruh group pada database baru
if [ -n "${DATABASE_URL:-}" ]; then
    echo "=== Mengaktifkan Hak Akses (View + Edit) untuk Testing Manual ==="
    psql "$DATABASE_URL" -q <<EOF
    -- Berikan level 2 (View + Edit) ke group_db_access untuk semua group & semua database
    INSERT INTO group_db_access (group_id, db_id, level)
    SELECT g.id, d.id, 2
    FROM groups g
    CROSS JOIN db_connections d
    ON CONFLICT (group_id, db_id) DO UPDATE SET level = 2;

    -- Bump privilege version agar rotasi session mendeteksi hak akses baru
    UPDATE users SET privilege_version = privilege_version + 1;
EOF
    echo " [OK] Matriks hak akses group telah diperbarui (Level 2: View + Edit)!"
fi

echo ""
echo "=========================================================================="
echo " SELURUH DATABASE TESTING TELAH TERHUBUNG DAN SIAP DIGUNAKAN DI SPA!"
echo " Buka browser di http://localhost:8080/databases untuk mulai eksplorasi."
echo "=========================================================================="
