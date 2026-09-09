# Architecture

The group assistant keeps Telegram transport, durable conversation state, AI orchestration, and scheduling rules separate.

```mermaid
flowchart TD
    TG[Telegram update] --> CLAIM[Receipt claim: token + lease]
    CLAIM --> ING[Conversation ingestion transaction]
    ING --> DB[(SQLite message graph)]
    ING --> ROUTE[Telegram routing]
    ROUTE --> CONV[Reply-chain context]
    CONV --> AGENT[Bounded Agent]
    AGENT --> LLM[OpenAI-compatible client]
    AGENT --> TOOLS[Whitelisted tools]
    TOOLS --> SCH[Schedule Service]
    SCH --> DB
    AGENT --> FIN[Owner-checked receipt finalization]
    FIN --> SEND[Telegram sender]
    SEND --> TG
    SEND --> DB
    ADMIN[Authorized admin private chat] --> ING
    SCH --> DASH[Dashboard/API immediately]
    SCH --> CHANGE[Change log + pending announcement queue]
    CHANGE --> SYNC[/sync]
    SYNC --> GROUP[Group chat digest]
```

## Layers

- `internal/telegram` is transport: update authorization, command parsing, rendering, and sending Telegram messages.
- `internal/conversation` is the source of truth for incoming and bot messages, reply edges, duplicate delivery handling, retention-safe pruning, and bounded context loading.
- `internal/agent` is a small bounded loop. It can call only registered tools and never receives a database handle.
- `internal/ai` is an OpenAI-compatible HTTP transport plus strict event-operation decoding.
- `internal/schedule` owns validation, snapshots, transactions, recurrence checks, conflicts, source records, and changelog writes.

## Message Graph And Retention

Every accepted user message and every successfully sent bot message is persisted after Telegram returns its `message_id`; persistence errors are logged with the chat and Telegram message IDs for operator recovery. Telegram send and SQLite persistence are separate systems and cannot be one global atomic transaction. A message has both the raw Telegram parent ID and, where known, a local parent ID. Persisted reply chains survive process restart and do not depend on Telegram returning historical replies. Insertion and late-parent edge repair run in one SQLite transaction.

`BuildReplyChain` walks parent links backward with a depth limit (default 16), character budget (default 3,000 bytes), missing-parent tolerance, and cycle protection. The conservative budget keeps the entire agent prompt below the transport's 6 KB input limit. `FindConversationRoot` separately traverses up to 128 parent edges with cycle protection to find an initiating `/event`; root ownership and explicit target authority come from this deterministic graph lookup, not prompt truncation. If no root is found within the bound, the bot asks for a new `/event`. Retention first nulls internal parent links of surviving children and then deletes old nodes. The raw Telegram parent ID remains for diagnostics, so retention never violates a graph foreign-key relationship.

## Routing And Conversation

The bot reacts to commands, replies to a persisted bot message, and no other ordinary group messages. An authorized private administrator can additionally ask ordinary questions and describe schedule mutations without `/event`; those mutations use the same structured parser and Schedule transaction as group commands. Private mutations are applied immediately but get a separate pending announcement record. `/sync preview` renders those records privately; `/sync` sends a group digest first and deletes the pending records only after Telegram confirms delivery. A process-local mutex prevents concurrent sync calls from normally duplicating a digest. `pending_intents` remains legacy storage only and is no longer in the conversational path.

## Agent And Tools

The agent accepts embedded system instructions, the reply chain, current time and timezone, and tool definitions. It may make one tool call per turn for at most four turns. Unknown tools, malformed responses, tool errors, and exhausted rounds fail closed.

Currently registered tools are `schedule_today` and `schedule_status`. New schedule mutation tools must construct a `schedule.Proposal` and call `schedule.Apply` or `schedule.ApplyAll`; they must not write SQLite directly.

## Event Safety

The model produces JSON proposals only. Go uses strict decoding with unknown-field rejection, candidate target checks, timezone/date/recurrence validation, target snapshots, and `schedule.ApplyAll` transactions. A multi-operation update validates and writes its events, tags, source records, and changelog records in one SQLite transaction. The model has no SQL, shell, or unrestricted service access.

## Update Processing Guarantees

Each accepted Telegram update first obtains a random receipt claim token and a short SQLite lease. The active handler renews its lease while it runs. Only that active owner performs ingestion, routing, agent calls, and schedule side effects. Completion and failure release are conditional on the same token, so a stale worker cannot finalize a later claim. An interrupted owner can be recovered after lease expiry. This provides durable at-most-one active owner of an update within this application, with lease recovery; it is not exactly-once execution across Telegram and SQLite.
