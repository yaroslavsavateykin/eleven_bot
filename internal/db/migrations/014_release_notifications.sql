CREATE TABLE release_notification_state (
 repository TEXT PRIMARY KEY,
 tag TEXT NOT NULL,
 updated_at TEXT NOT NULL
);
