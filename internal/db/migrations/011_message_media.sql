ALTER TABLE messages ADD COLUMN media_file_id TEXT;
ALTER TABLE messages ADD COLUMN media_mime_type TEXT;
CREATE INDEX IF NOT EXISTS messages_daily_media_idx ON messages(group_id,user_id,kind,sent_at);
