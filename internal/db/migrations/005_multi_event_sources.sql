ALTER TABLE event_sources RENAME TO event_sources_old;
CREATE TABLE event_sources (
 id INTEGER PRIMARY KEY,event_id INTEGER NOT NULL REFERENCES events(id),source_type TEXT NOT NULL,telegram_chat_id INTEGER,
 telegram_message_id INTEGER,external_id TEXT,raw_text TEXT,created_at TEXT NOT NULL,
 UNIQUE(source_type,external_id)
);
INSERT INTO event_sources(id,event_id,source_type,telegram_chat_id,telegram_message_id,external_id,raw_text,created_at)
SELECT id,event_id,source_type,telegram_chat_id,telegram_message_id,external_id,raw_text,created_at FROM event_sources_old;
DROP TABLE event_sources_old;
