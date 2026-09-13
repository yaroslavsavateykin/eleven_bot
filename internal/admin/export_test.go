package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"group411/internal/db"
)

func TestExportEndpoints(t *testing.T) {
	ctx := context.Background()
	d, err := db.Open(ctx, filepath.Join(t.TempDir(), "export.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	for _, q := range []string{
		`INSERT INTO groups(id,name,telegram_chat_id,timezone,dashboard_slug,created_at,updated_at) VALUES(1,'test',-1,'UTC','test','','')`,
		`INSERT INTO users(id,telegram_user_id,username,first_name,last_name,created_at,updated_at) VALUES(1,10,'alice','Алиса','','','')`,
		`INSERT INTO group_members(group_id,user_id,active,role,joined_at) VALUES(1,1,1,'member','')`,
		`INSERT INTO user_contexts(group_id,user_id,summary,tags_json,updated_at) VALUES(1,1,'любит химию','[]','')`,
		`INSERT INTO events(id,group_id,kind,category,title,starts_at,timezone,all_day,status,source_type,dedupe_key,created_at,updated_at) VALUES(1,1,'lesson','lesson','Семинар','2026-09-14T12:00:00Z','Europe/Moscow',0,'active','telegram','x','','')`,
	} {
		if _, err = d.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	h := Handler{DB: d, GroupID: 1, Password: "secret"}
	router := h.Router()

	for _, tc := range []struct{ path, want string }{
		{"/export/calendar", "Семинар"},
		{"/export/people", "любит химию"},
	} {
		req := httptest.NewRequest(http.MethodGet, tc.path, nil)
		req.SetBasicAuth("admin", "secret")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status=%d", tc.path, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), tc.want) {
			t.Fatalf("%s missing %q: %s", tc.path, tc.want, rec.Body.String())
		}
		if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/csv") {
			t.Fatalf("%s content-type=%q", tc.path, ct)
		}
	}

	// Unauthorized access is rejected.
	req := httptest.NewRequest(http.MethodGet, "/export/calendar", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status=%d", rec.Code)
	}
}
