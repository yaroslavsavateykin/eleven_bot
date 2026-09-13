# Architecture

The group assistant keeps Telegram transport, durable conversation state, AI orchestration, and scheduling rules separate.

```mermaid
flowchart TD
    TG[Telegram update] --> CLAIM[Receipt claim: token + lease]
    CLAIM --> ING[Conversation ingestion transaction]
    ING --> DB[(SQLite message graph)]
    ING --> ROUTE[Transport trigger policy]
    ROUTE --> CONV[Reply-chain context]
    CONV --> AGENT[Bounded Agent: Chat messages + native tools]
    AGENT --> LLM[OpenAI Chat Completions: tools + tool_choice auto]
    LLM --> CALL[Assistant tool_calls with provider IDs]
    CALL --> TOOLS[Registry + schema + auth validation]
    TOOLS --> SCH[Domain services + Apply/ApplyImport transactions]
    SCH --> RES[role=tool result with matching tool_call_id]
    RES --> LLM
    LLM --> FIN[Final natural language reply]
    FIN --> SEND[Telegram sender with provisional edit]
    SEND --> DB
    SEND --> TG
    ADMIN[Authorized admin private chat] --> ING
    SCH --> DASH[Dashboard/API immediately]
    SCH --> CHANGE[Change log + pending announcement queue]
    CHANGE --> SYNC[/sync]
    SYNC --> GROUP[Group chat digest]
```

## Layers

- `internal/telegram` is transport: update authorization, command parsing, reply graph metadata, rendering, and sending Telegram messages. It does not classify user intent.
- `internal/conversation` is the source of truth for incoming and bot messages, reply edges, duplicate delivery handling, retention-safe pruning, and bounded context loading.
- `internal/agent` is a small bounded loop. It can call only registered tools and never receives a database handle.
- `internal/ai` is an OpenAI-compatible HTTP, vision, and speech transport.
- `internal/schedule` owns validation, snapshots, transactions, recurrence checks, conflicts, source records, and changelog writes. Recurrence is a typed `RecurrenceSpec` at tool boundaries and is deterministically compiled to RRULE; events do not store academic-week parity.

## Message Graph And Retention

Every accepted user message and every successfully sent bot message is persisted after Telegram returns its `message_id`; persistence errors are logged with the chat and Telegram message IDs for operator recovery. Telegram send and SQLite persistence are separate systems and cannot be one global atomic transaction. A message has both the raw Telegram parent ID and, where known, a local parent ID. Persisted reply chains survive process restart and do not depend on Telegram returning historical replies. Insertion and late-parent edge repair run in one SQLite transaction.

`BuildReplyChain` walks parent links backward with a depth limit (default 16), character budget (default 3,000 bytes), missing-parent tolerance, and cycle protection. The conservative budget keeps the entire agent prompt below the transport's 6 KB input limit. `FindConversationRoot` separately traverses up to 128 parent edges with cycle protection to find an initiating `/event`; root ownership and explicit target authority come from this deterministic graph lookup, not prompt truncation. If no root is found within the bound, the bot asks for a new `/event`. Retention first nulls internal parent links of surviving children and then deletes old nodes. The raw Telegram parent ID remains for diagnostics, so retention never violates a graph foreign-key relationship.

## Routing And Conversation

The bot reacts to commands, replies to a persisted bot message, and no other ordinary group messages. Every activated natural-language message reaches the same Agent. An authorized private administrator receives mutation tools; ordinary group conversation receives read tools and explicit `/event` receives the write capability. Private mutations are applied immediately but get a separate pending announcement record. `/sync preview` renders those records privately; `/sync` sends a group digest first and deletes the pending records only after Telegram confirms delivery. A process-local mutex prevents concurrent sync calls from normally duplicating a digest.

## Agent And Tools

The agent sends system instructions, time/timezone, chronological user/assistant messages and native tools derived from Tool.Schema. There are ten tool rounds plus one final model turn by default. Multiple calls execute sequentially after registry authorization and schema validation. Assistant calls and role=tool results retain matching provider IDs. Tool errors are data, not instructions. Empty content with calls is valid; empty turns are protocol errors. JSON and SSE fragmented calls are decoded in internal/ai.

ModeGroup registers schedule_query and group_search. ModeGroupWrite and ModeAdminPrivate add schedule_create, schedule_create_batch, schedule_update, schedule_update_batch and schedule_cancel. Mutations server-resolve snapshots and use schedule.Service transactions.

AI_TOOL_MODE defaults to native (explicit legacy_json opt-in, no silent fallback). AI_CONTEXT_BYTES defaults to 131072 approximate bytes including schemas — not tokenizer tokens. Old input history is removed first; the current message, its immediate reply parent, and all tool exchanges are protected. Oversized protected context produces a controlled error. HTTP 429 and 5xx are retried; other 4xx are not. AI_STRICT_TOOLS and AI_DISABLE_PARALLEL_TOOLS are opt-in provider capabilities kept out of the agent loop; server validation and sequential execution remain mandatory.

Workflow: Telegram → Conversation context → Agent → native AI call → authorized registry → domain service → tool result (ok/data/error via role=tool) → AI → final human text → Telegram.

Mutation invocation keys combine stable Telegram chat/message identity and canonical tool arguments. agent_mutations stores results atomically with schedule changes, sources and changelog. Replay reads the result before target resolution (and after a crash with a changed snapshot). Best-effort batches have per-item transactional receipts. New Telegram messages use new scopes; different arguments are different invocations. This does not make Telegram delivery atomic with SQLite.

Tool results are compact (IDs, titles, times, rrule, 5 warnings max, 20 search hits × 600 chars) — never full DB dumps. Strict JSON validation uses DisallowUnknownFields, single-value EOF, duplicate-key detection, RFC3339 and bounded recurrence/length checks.

## Event Safety

The model produces JSON proposals only. Go uses strict decoding with unknown-field rejection, candidate target checks, timezone/date/recurrence validation, target snapshots, and `schedule.ApplyAll` transactions. A multi-operation update validates and writes its events, tags, source records, and changelog records in one SQLite transaction. The model has no SQL, shell, or unrestricted service access.

## Update Processing Guarantees

Each accepted Telegram update first obtains a random receipt claim token and a short SQLite lease. The active handler renews its lease while it runs. Only that active owner performs ingestion, routing, agent calls, and schedule side effects. Completion and failure release are conditional on the same token, so a stale worker cannot finalize a later claim. An interrupted owner can be recovered after lease expiry. This provides durable at-most-one active owner of an update within this application, with lease recovery; it is not exactly-once execution across Telegram and SQLite.
