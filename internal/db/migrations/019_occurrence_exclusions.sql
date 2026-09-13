CREATE TABLE event_exclusions (
 event_id INTEGER NOT NULL REFERENCES events(id) ON DELETE CASCADE,
 occurrence_date TEXT NOT NULL,
 created_at TEXT NOT NULL,
 PRIMARY KEY(event_id, occurrence_date)
);
