CREATE TABLE admin_schedule_notifications (
  change_id INTEGER PRIMARY KEY REFERENCES change_log(id),
  group_id INTEGER NOT NULL REFERENCES groups(id),
  created_at TEXT NOT NULL
);
CREATE INDEX admin_schedule_notifications_group_idx ON admin_schedule_notifications(group_id, change_id);
