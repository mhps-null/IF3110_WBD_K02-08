CREATE TABLE IF NOT EXISTS users (
    id BIGSERIAL PRIMARY KEY,
    username VARCHAR(100) NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    is_platform_admin BOOLEAN NOT NULL DEFAULT FALSE,
    is_active BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS sessions (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    id_hash VARCHAR(64) NOT NULL UNIQUE,
    prev_id_hash VARCHAR(64),
    prev_valid_until TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    rotated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    absolute_expires_at TIMESTAMPTZ NOT NULL,
    revoked_at TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_sessions_user_id ON sessions(user_id);
CREATE INDEX IF NOT EXISTS idx_sessions_prev_id_hash ON sessions(prev_id_hash);

CREATE TABLE IF NOT EXISTS db_connections (
    id BIGSERIAL PRIMARY KEY,
    name VARCHAR(100) NOT NULL,
    engine VARCHAR(20) NOT NULL CHECK (engine IN ('postgres', 'mysql', 'mariadb')),
    host VARCHAR(255) NOT NULL,
    port INTEGER NOT NULL,
    database_name VARCHAR(100) NOT NULL,
    username VARCHAR(100) NOT NULL,
    password_enc TEXT NOT NULL,
    ssl_mode VARCHAR(20) NOT NULL DEFAULT 'disable',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_db_connections_name ON db_connections(name);

CREATE TABLE IF NOT EXISTS groups (
    id BIGSERIAL PRIMARY KEY,
    name VARCHAR(100) NOT NULL UNIQUE,
    description TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS group_members (
    group_id BIGINT NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    PRIMARY KEY (group_id, user_id)
);

CREATE TABLE IF NOT EXISTS group_db_access (
    group_id BIGINT NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
    db_id BIGINT NOT NULL REFERENCES db_connections(id) ON DELETE CASCADE,
    level INTEGER NOT NULL CHECK (level IN (1, 2)), -- 1: View Only, 2: View + Edit
    PRIMARY KEY (group_id, db_id)
);

CREATE TABLE IF NOT EXISTS migrations (
    id BIGSERIAL PRIMARY KEY,
    db_id BIGINT NOT NULL REFERENCES db_connections(id) ON DELETE RESTRICT,
    version BIGINT, -- NULL for draft migrations
    title VARCHAR(255) NOT NULL,
    sql_text TEXT NOT NULL,
    status VARCHAR(20) NOT NULL CHECK (status IN ('draft', 'success', 'failed')),
    created_by BIGINT NOT NULL REFERENCES users(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    executed_by BIGINT REFERENCES users(id),
    executed_at TIMESTAMPTZ,
    error_message TEXT,
    checksum VARCHAR(64),
    CONSTRAINT uq_migrations_db_version UNIQUE (db_id, version)
);
CREATE INDEX IF NOT EXISTS idx_migrations_db_id ON migrations(db_id);
