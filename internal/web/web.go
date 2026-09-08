package web

import (
	"embed"
	"group411/internal/schedule"
	"html/template"
	"io/fs"
	"net/http"
	"time"
)

//go:embed templates/index.html static/style.css static/app.js
var files embed.FS

type Handler struct {
	Schedule  schedule.Service
	Name      string
	TZ        *time.Location
	Templates *template.Template
}

func Static() http.FileSystem { s, _ := fs.Sub(files, "static"); return http.FS(s) }
func New(s schedule.Service, name string, tz *time.Location) *Handler {
	return &Handler{Schedule: s, Name: name, TZ: tz, Templates: template.Must(template.New("").ParseFS(files, "templates/index.html"))}
}
func (h *Handler) Index(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.Templates.ExecuteTemplate(w, "index", map[string]any{"Name": h.Name, "TZ": h.TZ.String()}); err != nil {
		http.Error(w, "template error", 500)
	}
}
