CREATE VIRTUAL TABLE IF NOT EXISTS messages_fts USING fts5(text, tokenize='unicode61');

INSERT INTO messages_fts(rowid,text)
SELECT id,COALESCE(text,'') FROM messages
WHERE sender_type='user' AND COALESCE(text,'')<>''
  AND NOT EXISTS (SELECT 1 FROM messages_fts WHERE rowid=messages.id);

CREATE TRIGGER IF NOT EXISTS messages_fts_insert AFTER INSERT ON messages
WHEN NEW.sender_type='user' AND COALESCE(NEW.text,'')<>''
BEGIN
  INSERT INTO messages_fts(rowid,text) VALUES(NEW.id,NEW.text);
END;

CREATE TRIGGER IF NOT EXISTS messages_fts_update AFTER UPDATE OF text,sender_type ON messages
BEGIN
  DELETE FROM messages_fts WHERE rowid=OLD.id;
  INSERT INTO messages_fts(rowid,text)
  SELECT NEW.id,NEW.text WHERE NEW.sender_type='user' AND COALESCE(NEW.text,'')<>'';
END;

CREATE TRIGGER IF NOT EXISTS messages_fts_delete AFTER DELETE ON messages
BEGIN
  DELETE FROM messages_fts WHERE rowid=OLD.id;
END;
