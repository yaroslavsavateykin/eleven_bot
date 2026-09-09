ALTER TABLE telegram_receipts ADD COLUMN attempts INTEGER NOT NULL DEFAULT 0;
ALTER TABLE telegram_receipts ADD COLUMN last_error TEXT;
ALTER TABLE telegram_receipts ADD COLUMN processed_at TEXT;
