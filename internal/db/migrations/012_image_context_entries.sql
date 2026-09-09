CREATE TABLE image_context_entries (
  message_id INTEGER PRIMARY KEY REFERENCES messages(id),
  created_at TEXT NOT NULL
);
