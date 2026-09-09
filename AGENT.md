# Project Agent Rules

## Schedule Imports

- Keep ordinary `/event` requests atomic through `schedule.ApplyAll`.
- Treat ADMIN PRIVATE automatic schedule ingestion as best-effort: parse, normalize and validate each independent operation; apply valid operations; skip invalid or uncertain facts with a compact reason.
- An operation-level error must not prevent unrelated valid admin-import operations from being saved. Reserve fatal errors for AI/Telegram transport and database, transaction, migration, or other infrastructure failures.
- Preserve short source attribution (`source_index` and `source_text`) for imported operations so skipped records can be reported without exposing model JSON.
- Do not turn uncertain, historical, or non-calendar information into schedule mutations.

## Recurrence

- Normalize recurrence before deduplication and retain horizon provenance: `explicit_count`, `explicit_until`, `semester`, or `default`.
- Generated fallback `UNTIL` values are persistence bounds, not semantic identity. Identical recurring imports must deduplicate even if their generated horizon differs.
- In user-facing summaries, hide generated `UNTIL`: say `до конца семестра` for the semester horizon and `на ближайшие 16 недель` for the default horizon. Show a date only for explicit user-provided `UNTIL`.
- Use a configured semester horizon only when `DTSTART` is inside the typed configured semester. Otherwise use the default horizon.
- Prefer structured recurrence from AI output. Keep raw `rrule` only as a compatibility fallback.

## Maintaining This File

- When the user gives a new explicit, durable project rule, workflow requirement, or behavioral invariant, update this `AGENT.md` in the same change unless it is clearly a one-off request.
- Keep additions concise, actionable, and scoped to the project. Do not record secrets, transient debugging details, or duplicate general platform instructions.
