CREATE TABLE IF NOT EXISTS parse_errors (
 id INTEGER PRIMARY KEY,
 group_id INTEGER NOT NULL REFERENCES groups(id),
 telegram_chat_id INTEGER,
 telegram_message_id INTEGER,
 user_id INTEGER REFERENCES users(id),
 command TEXT NOT NULL,
 raw_text TEXT NOT NULL,
 error TEXT NOT NULL,
 created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS parse_errors_group_created_idx ON parse_errors(group_id, created_at DESC);
