package admin

import (
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"html/template"
	"net/http"
	"time"
)

type Handler struct {
	DB       *sql.DB
	GroupID  int64
	Password string
}

var page = template.Must(template.New("admin").Parse(`<!doctype html><meta charset="utf-8"><title>411 admin</title><style>body{font:16px system-ui;max-width:720px;margin:3rem auto;padding:0 1rem}input{padding:.5rem;width:100%;margin:.5rem 0}button{padding:.5rem 1rem}table{width:100%;border-collapse:collapse;margin-top:2rem}td,th{text-align:left;padding:.5rem;border-bottom:1px solid #ddd}</style><h1>411 группа: admin</h1><p>Известные участники и их роли.</p><table><tr><th>Имя</th><th>Username</th><th>Роль</th><th>Roast</th></tr>{{range .}}<tr><td>{{.Name}}</td><td>{{.Username}}</td><td>{{.Role}}</td><td>{{.Roast}}</td></tr>{{end}}</table>`))

func (h Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.Password == "" {
		http.Error(w, "ADMIN_PASSWORD is not configured", 503)
		return
	}
	u, p, ok := r.BasicAuth()
	expectedUser := "admin"
	if !ok || subtle.ConstantTimeCompare([]byte(u), []byte(expectedUser)) != 1 || subtle.ConstantTimeCompare(hash(p), hash(h.Password)) != 1 {
		w.Header().Set("WWW-Authenticate", `Basic realm="411 admin"`)
		http.Error(w, "authorization required", http.StatusUnauthorized)
		return
	}
	rows, err := h.DB.QueryContext(r.Context(), "SELECT COALESCE(u.first_name,''),COALESCE(u.username,''),m.role,m.roast_enabled FROM group_members m JOIN users u ON u.id=m.user_id WHERE m.group_id=? ORDER BY m.role DESC,u.first_name", h.GroupID)
	if err != nil {
		http.Error(w, "database error", 500)
		return
	}
	defer rows.Close()
	var members []struct{ Name, Username, Role, Roast string }
	for rows.Next() {
		var x struct{ Name, Username, Role, Roast string }
		var roast int
		if err = rows.Scan(&x.Name, &x.Username, &x.Role, &roast); err != nil {
			http.Error(w, "database error", 500)
			return
		}
		if roast == 1 {
			x.Roast = "on"
		} else {
			x.Roast = "off"
		}
		members = append(members, x)
	}
	page.Execute(w, members)
}
func hash(s string) []byte { x := sha256.Sum256([]byte(s)); return x[:] }

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
	return id, tx.Commit()
}
