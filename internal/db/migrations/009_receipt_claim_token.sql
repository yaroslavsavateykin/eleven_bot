ALTER TABLE telegram_receipts ADD COLUMN claim_token TEXT;
ALTER TABLE telegram_receipts ADD COLUMN lease_until TEXT;
UPDATE telegram_receipts SET lease_until=locked_until WHERE lease_until IS NULL;
