CREATE TABLE profile_refresh_state (
  group_id INTEGER PRIMARY KEY REFERENCES groups(id),
  refreshed_at TEXT NOT NULL
);
