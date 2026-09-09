CREATE TABLE group_announcement_changes (
 change_id INTEGER PRIMARY KEY REFERENCES change_log(id),
 group_id INTEGER NOT NULL REFERENCES groups(id),
 created_at TEXT NOT NULL
);
CREATE INDEX group_announcement_changes_group_idx ON group_announcement_changes(group_id,change_id);
