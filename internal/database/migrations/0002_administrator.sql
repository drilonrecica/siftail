CREATE TABLE administrators (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    username TEXT NOT NULL COLLATE BINARY
        CHECK (
            length(CAST(username AS BLOB)) BETWEEN 3 AND 64
            AND username NOT GLOB '*[^A-Za-z0-9._-]*'
        ),
    password_hash TEXT NOT NULL COLLATE BINARY
        CHECK (length(CAST(password_hash AS BLOB)) BETWEEN 64 AND 512),
    created_at_us INTEGER NOT NULL,
    password_changed_at_us INTEGER NOT NULL,
    disabled_at_us INTEGER
) STRICT;
