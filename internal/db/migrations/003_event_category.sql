ALTER TABLE events ADD COLUMN category TEXT NOT NULL DEFAULT 'other'
CHECK (category IN ('lesson','event','test','quiz','exam','deadline','other'));
