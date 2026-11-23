-- Large dataset seed for Milestone 2 batch migration performance testing.
-- Generates 500,000 rows so a single full-table UPDATE without batching takes more than 1 second.
\connect test_ecommerce_pg;

CREATE TABLE IF NOT EXISTS large_migration_records (
    id          BIGINT PRIMARY KEY,
    customer_id INTEGER NOT NULL,
    status      VARCHAR(32) NOT NULL DEFAULT 'pending',
    amount      NUMERIC(12,2) NOT NULL DEFAULT 100.00,
    payload     TEXT NOT NULL,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

INSERT INTO large_migration_records (id, customer_id, status, amount, payload, updated_at)
SELECT
    g AS id,
    (g % 1000) + 1 AS customer_id,
    CASE WHEN g % 3 = 0 THEN 'active' ELSE 'pending' END AS status,
    ((g % 500) + 10)::numeric(12,2) AS amount,
    md5(g::text) || '-' || md5((g * 7)::text) AS payload,
    NOW()
FROM generate_series(1, 500000) AS g
ON CONFLICT (id) DO NOTHING;

CREATE INDEX IF NOT EXISTS idx_large_migration_records_status ON large_migration_records (status);
ANALYZE large_migration_records;
