-- Message graph is intentionally retained independently of Telegram's short reply payload.
-- Parent IDs stay nullable so retention can delete old nodes without breaking descendants.
ALTER TABLE messages ADD COLUMN sender_type TEXT NOT NULL DEFAULT 'user';
ALTER TABLE messages ADD COLUMN reply_to_telegram_message_id INTEGER;
ALTER TABLE messages ADD COLUMN metadata_json TEXT NOT NULL DEFAULT '{}';
UPDATE messages SET reply_to_telegram_message_id=reply_to_message_id WHERE reply_to_message_id IS NOT NULL;
UPDATE messages
SET reply_to_message_id=(SELECT parent.id FROM messages parent WHERE parent.telegram_chat_id=messages.telegram_chat_id AND parent.telegram_message_id=messages.reply_to_telegram_message_id)
WHERE reply_to_telegram_message_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS messages_reply_telegram_idx ON messages(group_id,telegram_chat_id,reply_to_telegram_message_id);
CREATE INDEX IF NOT EXISTS messages_reply_internal_idx ON messages(reply_to_message_id);
