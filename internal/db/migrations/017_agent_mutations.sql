CREATE TABLE agent_mutations (
 group_id INTEGER NOT NULL REFERENCES groups(id),
 invocation_key TEXT NOT NULL,
 result_json TEXT NOT NULL,
 created_at TEXT NOT NULL,
 PRIMARY KEY(group_id, invocation_key)
);
