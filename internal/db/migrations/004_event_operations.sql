INSERT OR IGNORE INTO pending_intents(group_id,user_id,intent_type,payload_json,expires_at,created_at)
SELECT group_id,user_id,'event',payload_json,expires_at,created_at FROM pending_intents WHERE intent_type='add';
DELETE FROM pending_intents WHERE intent_type='add';
CREATE TABLE event_proposals (
 id TEXT PRIMARY KEY, group_id INTEGER NOT NULL REFERENCES groups(id),
 user_id INTEGER NOT NULL REFERENCES users(id), chat_id INTEGER NOT NULL,
 message_id INTEGER NOT NULL, payload_json TEXT NOT NULL, expires_at TEXT NOT NULL,
 created_at TEXT NOT NULL, consumed_at TEXT, UNIQUE(chat_id,message_id)
);
CREATE TABLE telegram_receipts(chat_id INTEGER NOT NULL,message_id INTEGER NOT NULL,created_at TEXT NOT NULL,PRIMARY KEY(chat_id,message_id));
