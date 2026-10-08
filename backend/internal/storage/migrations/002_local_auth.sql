CREATE TABLE users (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    username TEXT NOT NULL UNIQUE CHECK (username ~ '^[a-z0-9._-]{3,32}$'),
    password_hash TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE sessions (
    token_hash TEXT PRIMARY KEY CHECK (token_hash ~ '^[a-f0-9]{64}$'),
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX sessions_expiry_idx ON sessions (expires_at);
CREATE INDEX sessions_user_idx ON sessions (user_id);

ALTER TABLE notes ADD COLUMN user_id BIGINT REFERENCES users(id) ON DELETE CASCADE;
-- Legacy notes remain inaccessible until the first account claims them.
-- NOT VALID permits only the existing NULLs; new writes must have an owner.
ALTER TABLE notes ADD CONSTRAINT notes_user_required CHECK (user_id IS NOT NULL) NOT VALID;
DROP INDEX notes_order_idx;
CREATE INDEX notes_user_order_idx ON notes (user_id, pinned DESC, updated_at DESC, id DESC);
