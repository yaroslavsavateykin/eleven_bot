package api

import (
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"github.com/go-chi/chi/v5"
	"group411/internal/schedule"
	"net/http"
	"strconv"
	"time"
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
		v := r.Header.Get("Authorization")
		want := "Bearer " + a.Token
		if a.Token == "" || len(v) != len(want) || subtle.ConstantTimeCompare([]byte(v), []byte(want)) != 1 {
			http.Error(w, "unauthorized", 401)
			return
		}
		next.ServeHTTP(w, r)
	})
}
func write(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
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
	from := time.Now()
	to := from.AddDate(0, 0, 14)
	if x := r.URL.Query().Get("from"); x != "" {
		var err error
		from, err = time.Parse(time.RFC3339, x)
		if err != nil {
			http.Error(w, "invalid from", 400)
			return
		}
	}
	if x := r.URL.Query().Get("to"); x != "" {
		var err error
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
	write(w, map[string]any{"events": v})
}
func (a API) get(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	e := a.Schedule.Get(r.Context(), id)
	if e.ID == 0 {
		http.NotFound(w, r)
		return
	}
	write(w, e)
}
func (a API) create(w http.ResponseWriter, r *http.Request) {
	var e schedule.Event
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&e) != nil {
		http.Error(w, "invalid JSON", 400)
		return
	}
	e, dup, err := a.Schedule.Create(r.Context(), e, "api", r.Header.Get("Idempotency-Key"), "")
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	write(w, map[string]any{"status": map[bool]string{true: "duplicate", false: "created"}[dup], "event": e, "warnings": e.Warnings, "dashboard_url": a.BaseURL})
}
func (a API) ingest(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Text           string `json:"text"`
		Source         string `json:"source"`
		IdempotencyKey string `json:"idempotency_key"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in) != nil || in.Text == "" || in.IdempotencyKey == "" {
		http.Error(w, "text and idempotency_key required", 400)
		return
	}
	http.Error(w, "AI parser is not configured", 503)
}
func (a API) changes(w http.ResponseWriter, r *http.Request) {
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
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
		rows.Scan(&id, &k, &t, &ei, &p, &at)
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
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&x) != nil || x.Text == "" || x.RemindAt.IsZero() {
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
	_, e = a.DB.ExecContext(r.Context(), "INSERT INTO jobs(kind,payload_json,run_at,status,attempts,dedupe_key,created_at,updated_at) VALUES('fire_reminder',?,?,'pending',0,?,?,?)", `{"reminder_id":`+strconv.FormatInt(id, 10)+`}`, x.RemindAt.UTC().Format(time.RFC3339Nano), "reminder:"+strconv.FormatInt(id, 10), now, now)
	if e != nil {
		http.Error(w, "database error", 500)
		return
	}
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
