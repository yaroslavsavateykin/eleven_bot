ALTER TABLE telegram_receipts ADD COLUMN status TEXT NOT NULL DEFAULT 'processed';
ALTER TABLE telegram_receipts ADD COLUMN updated_at TEXT;
UPDATE telegram_receipts SET updated_at=created_at WHERE updated_at IS NULL;
