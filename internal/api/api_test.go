package api

import (
	"context"
	"encoding/json"
	"group411/internal/db"
	"group411/internal/schedule"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestEventCategoryAPI(t *testing.T) {
	d, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.Exec("INSERT INTO groups VALUES(1,'test',1,'UTC','test','now','now')"); err != nil {
		t.Fatal(err)
	}
	router := (API{DB: d, Schedule: schedule.Service{DB: d, GroupID: 1, TZ: time.UTC}, Token: "test"}).Router()
	for _, value := range []string{"quiz", "invalid"} {
		r := httptest.NewRequest("POST", "/events", strings.NewReader(`{"kind":"lesson","category":"`+value+`","title":"Assessment","starts_at":"2026-09-09T08:00:00Z"}`))
		r.Header.Set("Authorization", "Bearer test")
		r.Header.Set("Idempotency-Key", "test-category-"+value)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		if value == "invalid" {
			if w.Code != 400 {
				t.Fatal("invalid category accepted")
			}
			continue
		}
		var out struct {
			Event schedule.Event `json:"event"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		if w.Code != 200 || out.Event.Category != "quiz" {
			t.Fatal("category missing in create response")
		}
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("GET", "/public/v1/schedule?from=2026-09-09T00:00:00Z&to=2026-09-10T00:00:00Z", nil))
	var out struct {
		Events []schedule.Event `json:"events"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || len(out.Events) != 1 || out.Events[0].Category != "quiz" {
		t.Fatal("category missing in public schedule")
	}
}

func TestHermesAPIVersionAndIdempotency(t *testing.T) {
	d, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "api.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err = d.Exec("INSERT INTO groups VALUES(1,'test',1,'UTC','test','now','now')"); err != nil {
		t.Fatal(err)
	}
	router := (API{DB: d, Schedule: schedule.Service{DB: d, GroupID: 1, TZ: time.UTC}, Token: "test"}).Router()
	request := func(method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer test")
		r.Header.Set("Idempotency-Key", "overlap-"+body)
		for k, v := range headers {
			r.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	body := `{"kind":"lesson","title":"Physics","starts_at":"2026-09-09T08:00:00Z","ends_at":"2026-09-09T09:00:00Z","timezone":"UTC"}`
	created := request("POST", "/events", body, map[string]string{"Idempotency-Key": "secretary-message:42:action:0", "X-Source-Ref": "email:42"})
	if created.Code != http.StatusOK {
		t.Fatal(created.Code, created.Body.String())
	}
	var result struct {
		Event  schedule.Event `json:"event"`
		Status string         `json:"status"`
	}
	if err = json.Unmarshal(created.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Event.Version == "" {
		t.Fatal("event version missing")
	}
	retry := request("POST", "/events", body, map[string]string{"Idempotency-Key": "secretary-message:42:action:0"})
	if retry.Code != 200 {
		t.Fatal(retry.Code)
	}
	var replay struct {
		Event  schedule.Event `json:"event"`
		Status string         `json:"status"`
	}
	json.Unmarshal(retry.Body.Bytes(), &replay)
	if replay.Status != "duplicate" || replay.Event.ID != result.Event.ID {
		t.Fatalf("retry=%+v", replay)
	}
	stale := request("PUT", "/events/"+strconv.FormatInt(result.Event.ID, 10), body, map[string]string{"Idempotency-Key": "update-1", "If-Match": "stale"})
	if stale.Code != http.StatusPreconditionFailed {
		t.Fatalf("expected stale conflict: %d", stale.Code)
	}
	updated := request("PUT", "/events/"+strconv.FormatInt(result.Event.ID, 10), body, map[string]string{"Idempotency-Key": "update-1", "If-Match": result.Event.Version})
	if updated.Code != http.StatusOK {
		t.Fatal(updated.Code, updated.Body.String())
	}
	wrong := httptest.NewRequest("GET", "/events", nil)
	wrong.Header.Set("Authorization", "Bearer wrong")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, wrong)
	if w.Code != http.StatusUnauthorized {
		t.Fatal(w.Code)
	}
}

func TestCreateRejectsUnsupportedRecurrence(t *testing.T) {
	d, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "api.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err = d.Exec("INSERT INTO groups VALUES(1,'test',1,'UTC','test','now','now')"); err != nil {
		t.Fatal(err)
	}
	router := (API{DB: d, Schedule: schedule.Service{DB: d, GroupID: 1, TZ: time.UTC}, Token: "test"}).Router()
	r := httptest.NewRequest("POST", "/events", strings.NewReader(`{"kind":"lesson","title":"Physics","starts_at":"2026-09-09T08:00:00Z","ends_at":"2026-09-09T09:00:00Z","timezone":"UTC","rrule":"FREQ=HOURLY;COUNT=2"}`))
	r.Header.Set("Authorization", "Bearer test")
	r.Header.Set("Idempotency-Key", "hourly")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("unsupported recurrence accepted: %d %s", w.Code, w.Body.String())
	}
	var count int
	if err := d.QueryRow("SELECT COUNT(*) FROM events").Scan(&count); err != nil || count != 0 {
		t.Fatalf("invalid event persisted: count=%d err=%v", count, err)
	}
}

func TestOverlappingEventsReturnWarnings(t *testing.T) {
	d, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.Exec("INSERT INTO groups VALUES(1,'test',1,'UTC','test','now','now')"); err != nil {
		t.Fatal(err)
	}
	router := (API{DB: d, Schedule: schedule.Service{DB: d, GroupID: 1, TZ: time.UTC}, Token: "test"}).Router()
	post := func(body string) struct {
		Event    schedule.Event      `json:"event"`
		Warnings []schedule.Conflict `json:"warnings"`
	} {
		r := httptest.NewRequest("POST", "/events", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer test")
		r.Header.Set("Idempotency-Key", "overlap-"+body)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("create %d: %s", w.Code, w.Body.String())
		}
		var out struct {
			Event    schedule.Event      `json:"event"`
			Warnings []schedule.Conflict `json:"warnings"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	first := post(`{"kind":"event","title":"First","starts_at":"2026-09-09T08:00:00Z","ends_at":"2026-09-09T09:00:00Z","timezone":"UTC"}`)
	second := post(`{"kind":"event","title":"Second","starts_at":"2026-09-09T08:30:00Z","ends_at":"2026-09-09T09:30:00Z","timezone":"UTC"}`)
	if first.Event.ID == 0 || second.Event.ID == 0 || len(second.Warnings) != 1 || second.Warnings[0].Event.ID != first.Event.ID || second.Warnings[0].StartsAt.Format(time.RFC3339) != "2026-09-09T08:00:00Z" {
		t.Fatalf("overlap response: %#v", second)
	}
	var count int
	if err := d.QueryRow("SELECT count(*) FROM events WHERE group_id=1 AND status='active'").Scan(&count); err != nil || count != 2 {
		t.Fatalf("overlapping API events were not persisted: %d %v", count, err)
	}
}
