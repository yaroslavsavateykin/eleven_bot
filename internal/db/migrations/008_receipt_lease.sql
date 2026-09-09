ALTER TABLE telegram_receipts ADD COLUMN locked_until TEXT;
UPDATE telegram_receipts SET locked_until=updated_at WHERE locked_until IS NULL;
