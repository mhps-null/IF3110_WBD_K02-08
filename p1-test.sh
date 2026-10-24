#!/usr/bin/env bash
set -euo pipefail

BASE_URL="${BASE_URL:-http://localhost:8080}"
BASE_URL="${BASE_URL%/}"

: "${DEMO_USERNAME:?Set DEMO_USERNAME ke username akun yang akan didemokan}"
: "${DEMO_PASSWORD:?Set DEMO_PASSWORD ke password akun yang akan didemokan}"

# Avoid sending credentials over plain HTTP to a non-loopback host.
case "$BASE_URL" in
  https://*|http://localhost:8080|http://127.0.0.1:8080)
    ;;
  *)
    echo "Gunakan HTTPS, atau alamat HTTP lokal localhost:8080/127.0.0.1:8080." >&2
    exit 2
    ;;
esac

if ! command -v curl >/dev/null 2>&1; then
  echo "ERROR: curl harus terpasang." >&2
  exit 2
fi

tmpdir="$(mktemp -d)"
cookie_jar="$tmpdir/cookies.txt"
password_file="$tmpdir/demo-password"
trap 'rm -rf "$tmpdir"' EXIT

# Store the password in a private temporary file without a trailing newline.
(umask 077; printf '%s' "$DEMO_PASSWORD" > "$password_file")

request_status() {
  local label="$1"
  local expected="$2"
  local actual

  shift 2

  if ! actual="$(
    curl --silent --show-error \
      --connect-timeout 3 \
      --max-time 15 \
      --output "$tmpdir/response" \
      --write-out '%{http_code}' \
      "$@"
  )"; then
    printf 'FAIL %s: request gagal.\n' "$label" >&2
    exit 1
  fi

  if [[ "$actual" != "$expected" ]]; then
    printf 'FAIL %s: HTTP %s, seharusnya %s.\n' \
      "$label" "$actual" "$expected" >&2
    exit 1
  fi

  printf 'PASS %s (HTTP %s)\n' "$label" "$actual"
}

request_status "Health check" 200 \
  --cookie-jar "$cookie_jar" \
  "$BASE_URL/healthz"

request_status "Buka halaman login" 200 \
  --cookie "$cookie_jar" \
  --cookie-jar "$cookie_jar" \
  "$BASE_URL/login"

csrf_token="$(
  sed -n 's/.*name="csrf_token" value="\([^"]*\)".*/\1/p' \
    "$tmpdir/response" | head -n 1
)"

if [[ -z "$csrf_token" ]]; then
  echo "FAIL token CSRF tidak ditemukan pada form login." >&2
  exit 1
fi

request_status "POST login tanpa token CSRF ditolak" 403 \
  --request POST \
  --cookie "$cookie_jar" \
  --cookie-jar "$cookie_jar" \
  "$BASE_URL/login"

request_status "Login dengan kredensial dan CSRF valid" 303 \
  --cookie "$cookie_jar" \
  --cookie-jar "$cookie_jar" \
  --data-urlencode "username=$DEMO_USERNAME" \
  --data-urlencode "password@$password_file" \
  --data-urlencode "csrf_token=$csrf_token" \
  "$BASE_URL/login"

request_status "Akses beranda dengan session valid" 200 \
  --cookie "$cookie_jar" \
  --cookie-jar "$cookie_jar" \
  "$BASE_URL/"

if ! grep -Fq 'Autentikasi dan session berhasil.' "$tmpdir/response"; then
  echo "FAIL beranda tidak menampilkan konten terautentikasi yang diharapkan." >&2
  exit 1
fi

printf 'PASS konten beranda terautentikasi\n'

request_status "Logout dengan CSRF valid" 303 \
  --cookie "$cookie_jar" \
  --cookie-jar "$cookie_jar" \
  --data-urlencode "csrf_token=$csrf_token" \
  "$BASE_URL/logout"

request_status "Akses beranda setelah logout ditolak" 303 \
  --cookie "$cookie_jar" \
  --cookie-jar "$cookie_jar" \
  "$BASE_URL/"

printf '\nSemua pemeriksaan demo P1 lulus.\n'
