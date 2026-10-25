#!/bin/bash
set -e

# Create target test database for Schema Diff testing
psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" <<-EOSQL
    CREATE DATABASE test_ecommerce_pg_target;
    GRANT ALL PRIVILEGES ON DATABASE test_ecommerce_pg_target TO "$POSTGRES_USER";
EOSQL
