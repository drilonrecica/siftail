CREATE TABLE sessions (
    id INTEGER PRIMARY KEY,
    administrator_id INTEGER NOT NULL REFERENCES administrators(id) ON DELETE CASCADE,
    token_hash BLOB NOT NULL CHECK (length(token_hash) = 32),
    created_at_us INTEGER NOT NULL,
    last_used_at_us INTEGER NOT NULL CHECK (last_used_at_us >= created_at_us),
    expires_at_us INTEGER NOT NULL CHECK (expires_at_us > created_at_us),
    revoked_at_us INTEGER,
    user_agent_summary TEXT
        CHECK (user_agent_summary IS NULL OR length(CAST(user_agent_summary AS BLOB)) BETWEEN 1 AND 256),
    client_identity_summary TEXT
        CHECK (client_identity_summary IS NULL OR length(CAST(client_identity_summary AS BLOB)) BETWEEN 1 AND 128),
    UNIQUE (token_hash)
) STRICT;

CREATE INDEX sessions_active_lru_idx
ON sessions(administrator_id, last_used_at_us, created_at_us, id)
WHERE revoked_at_us IS NULL;
