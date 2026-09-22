package api

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"group411/internal/schedule"
)

type API struct {
	DB             *sql.DB
	Schedule       schedule.Service
	Token, BaseURL string
}

func (a API) Router() http.Handler {
	r := chi.NewRouter()
	r.Get("/public/v1/status", a.status)
	r.Get("/public/v1/schedule", a.list)
	r.Group(func(r chi.Router) {
		r.Use(a.auth)
		r.Get("/events", a.list)
		r.Get("/events/{id}", a.get)
		r.Post("/events", a.create)
		r.Put("/events/{id}", a.update)
		r.Delete("/events/{id}", a.cancel)
		r.Post("/events/{id}/exclude-occurrence", a.excludeOccurrence)
		r.Post("/events/batch", a.batch)
		r.Post("/ingest", a.ingest)
		r.Get("/changes", a.changes)
		r.Get("/reminders", a.reminders)
		r.Post("/reminders", a.createReminder)
		r.Get("/users", a.users)
		r.Get("/users/{id}/context", a.context)
	})
	return r
}
func (a API) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		v, want := r.Header.Get("Authorization"), "Bearer "+a.Token
		if a.Token == "" || len(v) != len(want) || subtle.ConstantTimeCompare([]byte(v), []byte(want)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}
func write(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
func apiError(w http.ResponseWriter, code int, message string, current schedule.Event) {
	payload := map[string]any{"error": message}
	if current.ID != 0 {
		payload["current_event"] = current
		w.Header().Set("ETag", current.Version)
	}
	writeStatus(w, code, payload)
}
func writeStatus(w http.ResponseWriter, code int, v any) { w.WriteHeader(code); write(w, v) }
func strictJSON(w http.ResponseWriter, r *http.Request, target any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(target); err != nil {
		return err
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("request body must contain one JSON object")
	}
	return nil
}
func eventID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "invalid event id", 400)
		return 0, false
	}
	return id, true
}
func expectedVersion(r *http.Request) string {
	return strings.Trim(strings.TrimSpace(r.Header.Get("If-Match")), `"`)
}
func mutationContext(r *http.Request, suffix string) (context.Context, string, error) {
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" {
		return nil, "", fmt.Errorf("Idempotency-Key is required")
	}
	if len(key) > 512 {
		return nil, "", fmt.Errorf("Idempotency-Key is too long")
	}
	if suffix != "" {
		key += suffix
	}
	ref := strings.TrimSpace(r.Header.Get("X-Source-Ref"))
	if len(ref) > 4000 {
		return nil, "", fmt.Errorf("X-Source-Ref is too long")
	}
	ctx := schedule.WithInvocation(r.Context(), "hermes:"+key)
	ctx = schedule.WithMutationContext(ctx, schedule.MutationContext{SourceType: "hermes", ExternalID: key, SourceRef: ref})
	return ctx, key, nil
}
func responseEvent(w http.ResponseWriter, status string, event schedule.Event, warnings []schedule.Conflict, baseURL string) {
	event.Version = schedule.Version(event)
	w.Header().Set("ETag", event.Version)
	write(w, map[string]any{"status": status, "event": event, "warnings": warnings, "dashboard_url": baseURL})
}

func (a API) status(w http.ResponseWriter, r *http.Request) {
	v, e := a.Schedule.CurrentStatus(r.Context(), time.Now())
	if e != nil {
		http.Error(w, "database error", 500)
		return
	}
	write(w, v)
}
func (a API) list(w http.ResponseWriter, r *http.Request) {
	from, to := time.Now(), time.Now().AddDate(0, 0, 14)
	var err error
	if x := r.URL.Query().Get("from"); x != "" {
		from, err = time.Parse(time.RFC3339, x)
		if err != nil {
			http.Error(w, "invalid from", 400)
			return
		}
	}
	if x := r.URL.Query().Get("to"); x != "" {
		to, err = time.Parse(time.RFC3339, x)
		if err != nil {
			http.Error(w, "invalid to", 400)
			return
		}
	}
	if !to.After(from) || to.Sub(from) > 366*24*time.Hour {
		http.Error(w, "range must be positive and at most 366 days", 400)
		return
	}
	v, e := a.Schedule.List(r.Context(), from, to, nil)
	if e != nil {
		http.Error(w, "database error", 500)
		return
	}
	// List expands recurring series into occurrences. Their optimistic-locking
	// token still belongs to the underlying series and is set by Schedule.List.
	write(w, map[string]any{"events": v})
}
func (a API) get(w http.ResponseWriter, r *http.Request) {
	id, ok := eventID(w, r)
	if !ok {
		return
	}
	e := a.Schedule.Get(r.Context(), id)
	if e.ID == 0 {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("ETag", e.Version)
	write(w, e)
}
func (a API) create(w http.ResponseWriter, r *http.Request) {
	var e schedule.Event
	if strictJSON(w, r, &e) != nil {
		http.Error(w, "invalid JSON", 400)
		return
	}
	ctx, _, err := mutationContext(r, "")
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	e, dup, err := a.Schedule.Create(ctx, e, "hermes", strings.TrimSpace(r.Header.Get("Idempotency-Key")), strings.TrimSpace(r.Header.Get("X-Source-Ref")))
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	responseEvent(w, map[bool]string{true: "duplicate", false: "created"}[dup], e, e.Warnings, a.BaseURL)
}
func (a API) update(w http.ResponseWriter, r *http.Request) {
	id, ok := eventID(w, r)
	if !ok {
		return
	}
	ctx, _, err := mutationContext(r, "")
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if replay, found, replayErr := a.Schedule.InvocationResult(ctx); replayErr != nil {
		http.Error(w, "database error", 500)
		return
	} else if found && len(replay) == 1 {
		responseEvent(w, "duplicate", replay[0], replay[0].Warnings, a.BaseURL)
		return
	}
	current := a.Schedule.Get(r.Context(), id)
	if current.ID == 0 {
		http.NotFound(w, r)
		return
	}
	if expected := expectedVersion(r); expected == "" {
		apiError(w, 428, "If-Match is required", current)
		return
	} else if expected != current.Version {
		apiError(w, 412, "event version is stale", current)
		return
	}
	var e schedule.Event
	if strictJSON(w, r, &e) != nil {
		http.Error(w, "invalid JSON", 400)
		return
	}
	e.ID = id
	e.GroupID = a.Schedule.GroupID
	result, err := a.Schedule.Apply(ctx, schedule.Proposal{Operation: "update", Event: e, Before: schedule.Snapshot(current)}, 0, 0)
	if err != nil {
		if errors.Is(err, schedule.ErrStaleVersion) || strings.Contains(err.Error(), "изменилось") {
			apiError(w, 412, "event version is stale", a.Schedule.Get(r.Context(), id))
			return
		}
		http.Error(w, err.Error(), 400)
		return
	}
	responseEvent(w, "updated", result, result.Warnings, a.BaseURL)
}
func (a API) cancel(w http.ResponseWriter, r *http.Request) {
	id, ok := eventID(w, r)
	if !ok {
		return
	}
	ctx, _, err := mutationContext(r, "")
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if replay, found, replayErr := a.Schedule.InvocationResult(ctx); replayErr != nil {
		http.Error(w, "database error", 500)
		return
	} else if found && len(replay) == 1 {
		responseEvent(w, "duplicate", replay[0], replay[0].Warnings, a.BaseURL)
		return
	}
	current := a.Schedule.Get(r.Context(), id)
	if current.ID == 0 {
		http.NotFound(w, r)
		return
	}
	if expected := expectedVersion(r); expected == "" {
		apiError(w, 428, "If-Match is required", current)
		return
	} else if expected != current.Version {
		apiError(w, 412, "event version is stale", current)
		return
	}
	result, err := a.Schedule.Apply(ctx, schedule.Proposal{Operation: "cancel", Event: current, Before: schedule.Snapshot(current)}, 0, 0)
	if err != nil {
		apiError(w, 412, "event version is stale", a.Schedule.Get(r.Context(), id))
		return
	}
	responseEvent(w, "cancelled", result, nil, a.BaseURL)
}
func (a API) excludeOccurrence(w http.ResponseWriter, r *http.Request) {
	id, ok := eventID(w, r)
	if !ok {
		return
	}
	ctx, _, err := mutationContext(r, "")
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	current := a.Schedule.Get(r.Context(), id)
	if current.ID == 0 {
		http.NotFound(w, r)
		return
	}
	var input struct {
		OccurrenceDate  string `json:"occurrence_date"`
		ExpectedVersion string `json:"expected_version"`
	}
	if strictJSON(w, r, &input) != nil || input.OccurrenceDate == "" {
		http.Error(w, "occurrence_date is required", 400)
		return
	}
	expected := expectedVersion(r)
	if expected == "" {
		expected = input.ExpectedVersion
	}
	if expected == "" {
		apiError(w, 428, "If-Match is required", current)
		return
	}
	result, err := a.Schedule.ExcludeOccurrenceVersioned(ctx, id, input.OccurrenceDate, expected, false)
	if err != nil {
		if errors.Is(err, schedule.ErrStaleVersion) {
			apiError(w, 412, "event version is stale", a.Schedule.Get(r.Context(), id))
			return
		}
		http.Error(w, err.Error(), 400)
		return
	}
	responseEvent(w, "excluded", result, nil, a.BaseURL)
}

type batchItem struct {
	Operation       string          `json:"operation"`
	Event           *schedule.Event `json:"event,omitempty"`
	EventID         int64           `json:"event_id,omitempty"`
	ExpectedVersion string          `json:"expected_version,omitempty"`
	OccurrenceDate  string          `json:"occurrence_date,omitempty"`
}

func (a API) batch(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Operations []batchItem `json:"operations"`
	}
	if strictJSON(w, r, &input) != nil || len(input.Operations) == 0 || len(input.Operations) > 100 {
		http.Error(w, "operations (1..100) are required", 400)
		return
	}
	ctx, key, err := mutationContext(r, "")
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	results := make([]map[string]any, 0, len(input.Operations))
	for i, item := range input.Operations {
		itemCtx := schedule.WithInvocation(ctx, fmt.Sprintf("hermes:%s:item:%d", key, i))
		itemCtx = schedule.WithMutationContext(itemCtx, schedule.MutationContext{SourceType: "hermes", ExternalID: fmt.Sprintf("%s:item:%d", key, i), SourceRef: strings.TrimSpace(r.Header.Get("X-Source-Ref"))})
		out := map[string]any{"index": i, "operation": item.Operation}
		// A retry can arrive after the first request committed but before its
		// response reached Hermes. Replay the durable per-item receipt before
		// resolving the current version, which has legitimately changed.
		if replay, found, replayErr := a.Schedule.InvocationResult(itemCtx); replayErr != nil {
			out["status"] = "error"
			out["error"] = "database error"
			results = append(results, out)
			continue
		} else if found && len(replay) == 1 {
			out["status"] = "duplicate"
			out["event"] = replay[0]
			results = append(results, out)
			continue
		}
		switch item.Operation {
		case "create":
			if item.Event == nil {
				out["status"] = "error"
				out["error"] = "event is required"
				break
			}
			event, dup, createErr := a.Schedule.Create(itemCtx, *item.Event, "hermes", fmt.Sprintf("%s:item:%d", key, i), strings.TrimSpace(r.Header.Get("X-Source-Ref")))
			if createErr != nil {
				out["status"] = "error"
				out["error"] = createErr.Error()
			} else {
				out["status"] = map[bool]string{true: "duplicate", false: "created"}[dup]
				out["event"] = event
			}
		case "update", "cancel":
			current := a.Schedule.Get(r.Context(), item.EventID)
			if current.ID == 0 {
				out["status"] = "error"
				out["error"] = "event not found"
				break
			}
			if item.ExpectedVersion == "" || item.ExpectedVersion != current.Version {
				out["status"] = "conflict"
				out["current_event"] = current
				break
			}
			proposal := schedule.Proposal{Operation: item.Operation, Event: current, Before: schedule.Snapshot(current)}
			if item.Operation == "update" {
				if item.Event == nil {
					out["status"] = "error"
					out["error"] = "event is required"
					break
				}
				proposal.Event = *item.Event
				proposal.Event.ID = current.ID
				proposal.Event.GroupID = a.Schedule.GroupID
			}
			event, applyErr := a.Schedule.Apply(itemCtx, proposal, 0, 0)
			if applyErr != nil {
				out["status"] = "error"
				out["error"] = applyErr.Error()
			} else {
				out["status"] = map[string]string{"update": "updated", "cancel": "cancelled"}[item.Operation]
				out["event"] = event
			}
		case "exclude_occurrence":
			current := a.Schedule.Get(r.Context(), item.EventID)
			if current.ID == 0 || item.ExpectedVersion == "" || item.ExpectedVersion != current.Version {
				out["status"] = "conflict"
				out["current_event"] = current
				break
			}
			event, excludeErr := a.Schedule.ExcludeOccurrenceVersioned(itemCtx, item.EventID, item.OccurrenceDate, item.ExpectedVersion, false)
			if excludeErr != nil {
				out["status"] = "error"
				out["error"] = excludeErr.Error()
			} else {
				out["status"] = "excluded"
				out["event"] = event
			}
		default:
			out["status"] = "error"
			out["error"] = "unsupported operation"
		}
		results = append(results, out)
	}
	write(w, map[string]any{"results": results})
}
func (a API) ingest(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Text           string `json:"text"`
		Source         string `json:"source"`
		IdempotencyKey string `json:"idempotency_key"`
	}
	if strictJSON(w, r, &in) != nil || in.Text == "" || in.IdempotencyKey == "" {
		http.Error(w, "text and idempotency_key required", 400)
		return
	}
	http.Error(w, "AI parser is not configured", 503)
}
func (a API) changes(w http.ResponseWriter, r *http.Request) {
	after, err := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	if r.URL.Query().Get("after") != "" && err != nil || after < 0 {
		http.Error(w, "invalid after", 400)
		return
	}
	rows, e := a.DB.QueryContext(r.Context(), "SELECT id,kind,entity_type,entity_id,payload_json,created_at FROM change_log WHERE group_id=? AND id>? ORDER BY id LIMIT 200", a.Schedule.GroupID, after)
	if e != nil {
		http.Error(w, "database error", 500)
		return
	}
	defer rows.Close()
	var out []map[string]any
	next := after
	for rows.Next() {
		var id int64
		var k, t, ei, p, at string
		if rows.Scan(&id, &k, &t, &ei, &p, &at) != nil {
			http.Error(w, "database error", 500)
			return
		}
		out = append(out, map[string]any{"id": id, "kind": k, "entity_type": t, "entity_id": ei, "payload_json": json.RawMessage(p), "created_at": at})
		next = id
	}
	write(w, map[string]any{"changes": out, "next_cursor": next})
}
func (a API) reminders(w http.ResponseWriter, r *http.Request) {
	rows, e := a.DB.QueryContext(r.Context(), "SELECT id,text,remind_at,next_fire_at,status FROM reminders WHERE group_id=?", a.Schedule.GroupID)
	if e != nil {
		http.Error(w, "database error", 500)
		return
	}
	defer rows.Close()
	var x []map[string]any
	for rows.Next() {
		var id int64
		var text, ra, nf, st string
		rows.Scan(&id, &text, &ra, &nf, &st)
		x = append(x, map[string]any{"id": id, "text": text, "remind_at": ra, "next_fire_at": nf, "status": st})
	}
	write(w, map[string]any{"reminders": x})
}
func (a API) createReminder(w http.ResponseWriter, r *http.Request) {
	var x struct {
		Text         string    `json:"text"`
		RemindAt     time.Time `json:"remind_at"`
		TargetUserID *int64    `json:"target_user_id"`
		RRule        *string   `json:"rrule"`
	}
	if strictJSON(w, r, &x) != nil || x.Text == "" || x.RemindAt.IsZero() {
		http.Error(w, "text and remind_at required", 400)
		return
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	res, e := a.DB.ExecContext(r.Context(), "INSERT INTO reminders(group_id,target_user_id,text,remind_at,timezone,rrule,next_fire_at,status,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?)", a.Schedule.GroupID, x.TargetUserID, x.Text, x.RemindAt.UTC().Format(time.RFC3339Nano), a.Schedule.TZ.String(), x.RRule, x.RemindAt.UTC().Format(time.RFC3339Nano), "active", now, now)
	if e != nil {
		http.Error(w, "database error", 500)
		return
	}
	id, _ := res.LastInsertId()
	write(w, map[string]any{"id": id, "dashboard_url": a.BaseURL})
}
func (a API) users(w http.ResponseWriter, r *http.Request) {
	rows, e := a.DB.QueryContext(r.Context(), "SELECT u.id,u.telegram_user_id,u.username,u.first_name FROM users u JOIN group_members m ON m.user_id=u.id WHERE m.group_id=? AND m.active=1", a.Schedule.GroupID)
	if e != nil {
		http.Error(w, "database error", 500)
		return
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var id, tid int64
		var un, fn sql.NullString
		rows.Scan(&id, &tid, &un, &fn)
		out = append(out, map[string]any{"id": id, "telegram_user_id": tid, "username": un, "first_name": fn})
	}
	write(w, map[string]any{"users": out})
}
func (a API) context(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var summary, tags string
	e := a.DB.QueryRowContext(r.Context(), "SELECT summary,tags_json FROM user_contexts WHERE group_id=? AND user_id=?", a.Schedule.GroupID, id).Scan(&summary, &tags)
	if e == sql.ErrNoRows {
		http.NotFound(w, r)
		return
	}
	if e != nil {
		http.Error(w, "database error", 500)
		return
	}
	write(w, map[string]any{"user_id": id, "summary": summary, "tags": json.RawMessage(tags)})
}
