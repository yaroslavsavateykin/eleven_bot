package admin

import (
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/csv"
	"html/template"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
)

type Handler struct {
	DB       *sql.DB
	GroupID  int64
	Password string
}

func (h Handler) Router() http.Handler {
	r := chi.NewRouter()
	r.Use(h.auth)
	r.Get("/", h.page)
	r.Get("/export/calendar", h.exportCalendar)
	r.Get("/export/people", h.exportPeople)
	return r
}

func (h Handler) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h.Password == "" {
			http.Error(w, "ADMIN_PASSWORD is not configured", http.StatusServiceUnavailable)
			return
		}
		u, p, ok := r.BasicAuth()
		if !ok || subtle.ConstantTimeCompare([]byte(u), []byte("admin")) != 1 || subtle.ConstantTimeCompare(hash(p), hash(h.Password)) != 1 {
			w.Header().Set("WWW-Authenticate", `Basic realm="411 admin"`)
			http.Error(w, "authorization required", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

var page = template.Must(template.New("admin").Parse(`<!doctype html><meta charset="utf-8"><title>411 admin</title><style>body{font:16px system-ui;max-width:720px;margin:3rem auto;padding:0 1rem}input{padding:.5rem;width:100%;margin:.5rem 0}button{padding:.5rem 1rem}table{width:100%;border-collapse:collapse;margin-top:2rem}td,th{text-align:left;padding:.5rem;border-bottom:1px solid #ddd}a{display:inline-block;margin:1rem 0;padding:.5rem .75rem;background:#eee;border-radius:4px;text-decoration:none;color:#000}</style><h1>411 группа: admin</h1><nav><a href="/admin/export/calendar">Выгрузить календарь (CSV)</a> <a href="/admin/export/people">Выгрузить сводку по людям (CSV)</a></nav><p>Известные участники и их роли.</p><table><tr><th>Имя</th><th>Username</th><th>Роль</th><th>Roast</th></tr>{{range .}}<tr><td>{{.Name}}</td><td>{{.Username}}</td><td>{{.Role}}</td><td>{{.Roast}}</td></tr>{{end}}</table>`))

func (h Handler) page(w http.ResponseWriter, r *http.Request) {
	rows, err := h.DB.QueryContext(r.Context(), "SELECT COALESCE(u.first_name,''),COALESCE(u.username,''),m.role,m.roast_enabled FROM group_members m JOIN users u ON u.id=m.user_id WHERE m.group_id=? ORDER BY m.role DESC,u.first_name", h.GroupID)
	if err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	var members []struct{ Name, Username, Role, Roast string }
	for rows.Next() {
		var x struct{ Name, Username, Role, Roast string }
		var roast int
		if err = rows.Scan(&x.Name, &x.Username, &x.Role, &roast); err != nil {
			http.Error(w, "database error", http.StatusInternalServerError)
			return
		}
		x.Roast = map[bool]string{true: "on", false: "off"}[roast == 1]
		members = append(members, x)
	}
	page.Execute(w, members)
}

// exportCalendar streams every event of the group (active and cancelled) as CSV.
func (h Handler) exportCalendar(w http.ResponseWriter, r *http.Request) {
	rows, err := h.DB.QueryContext(r.Context(), `SELECT e.id,e.kind,e.category,e.title,e.description,e.location,e.starts_at,e.ends_at,e.timezone,e.all_day,e.rrule,e.recurrence_horizon,e.status,e.deleted_at,e.created_at,e.updated_at,COALESCE(group_concat(t.name),'') FROM events e LEFT JOIN event_tags et ON et.event_id=e.id LEFT JOIN tags t ON t.id=et.tag_id WHERE e.group_id=? GROUP BY e.id ORDER BY e.starts_at,e.id`, h.GroupID)
	if err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="calendar.csv"`)
	cw := csv.NewWriter(w)
	if err := cw.Write([]string{"id", "kind", "category", "title", "description", "location", "starts_at", "ends_at", "timezone", "all_day", "rrule", "recurrence_horizon", "status", "deleted_at", "tags", "created_at", "updated_at"}); err != nil {
		return
	}
	for rows.Next() {
		var (
			id                                                                           int64
			kind, category, title, startsAt, timezone, status                            string
			description, location, endsAt, rrule, horizon, deletedAt, tags, created, upd sql.NullString
			allDay                                                                       int
		)
		if err := rows.Scan(&id, &kind, &category, &title, &description, &location, &startsAt, &endsAt, &timezone, &allDay, &rrule, &horizon, &status, &deletedAt, &created, &upd, &tags); err != nil {
			http.Error(w, "database error", http.StatusInternalServerError)
			return
		}
		record := []string{
			itoa(id), kind, category, title,
			description.String, location.String, startsAt, endsAt.String, timezone,
			itoa(int64(allDay)), rrule.String, horizon.String, status, deletedAt.String,
			tags.String, created.String, upd.String,
		}
		if err := cw.Write(record); err != nil {
			return
		}
	}
	if err := rows.Err(); err != nil {
		return
	}
	cw.Flush()
}

// exportPeople streams the per-user summaries (image/context notes) as CSV.
func (h Handler) exportPeople(w http.ResponseWriter, r *http.Request) {
	rows, err := h.DB.QueryContext(r.Context(), `SELECT COALESCE(u.first_name,''),COALESCE(u.username,''),m.role,COALESCE(uc.summary,''),COALESCE(uc.tags_json,'[]'),COALESCE(uc.updated_at,'') FROM group_members m JOIN users u ON u.id=m.user_id LEFT JOIN user_contexts uc ON uc.group_id=m.group_id AND uc.user_id=u.id WHERE m.group_id=? AND m.active=1 ORDER BY u.first_name`, h.GroupID)
	if err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="people.csv"`)
	cw := csv.NewWriter(w)
	if err := cw.Write([]string{"name", "username", "role", "summary", "tags", "updated_at"}); err != nil {
		return
	}
	for rows.Next() {
		var name, username, role, summary, tags, updated string
		if err := rows.Scan(&name, &username, &role, &summary, &tags, &updated); err != nil {
			http.Error(w, "database error", http.StatusInternalServerError)
			return
		}
		if err := cw.Write([]string{name, username, role, summary, tags, updated}); err != nil {
			return
		}
	}
	if err := rows.Err(); err != nil {
		return
	}
	cw.Flush()
}

func hash(s string) []byte { x := sha256.Sum256([]byte(s)); return x[:] }

func itoa(v int64) string { return strconv.FormatInt(v, 10) }

// EnsureMember binds administrative authority to the configured Telegram identity, never a username.
func EnsureMember(r *sql.DB, groupID, telegramID int64, username, firstName, lastName string, ownerID int64) (int64, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	tx, err := r.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	_, err = tx.Exec("INSERT INTO users(telegram_user_id,username,first_name,last_name,last_seen_at,created_at,updated_at) VALUES(?,?,?,?,?,?,?) ON CONFLICT(telegram_user_id) DO UPDATE SET username=excluded.username,first_name=excluded.first_name,last_name=excluded.last_name,last_seen_at=excluded.last_seen_at,updated_at=excluded.updated_at", telegramID, username, firstName, lastName, now, now, now)
	if err != nil {
		return 0, err
	}
	var id int64
	if err = tx.QueryRow("SELECT id FROM users WHERE telegram_user_id=?", telegramID).Scan(&id); err != nil {
		return 0, err
	}
	role := "member"
	if ownerID > 0 && telegramID == ownerID {
		role = "admin"
	}
	_, err = tx.Exec("INSERT INTO group_members(group_id,user_id,active,role,joined_at,last_seen_at) VALUES(?,?,1,?,?,?) ON CONFLICT(group_id,user_id) DO UPDATE SET active=1,role=excluded.role,last_seen_at=excluded.last_seen_at", groupID, id, role, now, now)
	if err != nil {
		return 0, err
	}
	if _, err = tx.Exec("INSERT OR IGNORE INTO user_contexts(group_id,user_id,summary,tags_json,updated_at) VALUES(?,?,?,'[]',?)", groupID, id, "", now); err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

// MarkMemberInactive keeps a departed participant out of group-wide mentions
// without deleting their historical messages or profile.
func MarkMemberInactive(r *sql.DB, groupID, telegramID int64) error {
	_, err := r.Exec(`UPDATE group_members SET active=0 WHERE group_id=?
		AND user_id=(SELECT id FROM users WHERE telegram_user_id=?)`, groupID, telegramID)
	return err
}
