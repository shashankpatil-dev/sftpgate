package web

import (
	"embed"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"time"

	"github.com/shashankpatil-dev/sftpgate/internal/actions"
	"github.com/shashankpatil-dev/sftpgate/internal/audit"
	"github.com/shashankpatil-dev/sftpgate/internal/server"
)

//go:embed templates/*.html templates/style.css
var files embed.FS

// pages holds one template per page, each built on layout.html.
var pages = map[string]*template.Template{}

func init() {
	funcs := template.FuncMap{
		"ago":   ago,
		"bytes": humanBytes,
		"short": func(s string) string { return s[:min(len(s), 12)] },
		"when":  func(t time.Time) string { return t.Local().Format("2006-01-02 15:04:05") },
	}
	for _, name := range []string{"overview", "events", "actions", "config"} {
		pages[name] = template.Must(template.New("layout.html").Funcs(funcs).
			ParseFS(files, "templates/layout.html", "templates/"+name+".html"))
	}
}

// page is what every page template gets.
type page struct {
	Title   string
	Nav     string
	Refresh bool
	Status  Status

	Sessions []server.SessionInfo
	Events   []audit.Event
	Filter   audit.Filter

	// EventNames and Results fill the filter menus on the events page.
	EventNames []string
	Results    []string
	Pending    []actions.Job
	Failed     []actions.Job
	Config     string
	Error      string
}

func (s *site) render(w http.ResponseWriter, r *http.Request, name string, p page) {
	p.Nav = name
	p.Status = s.status(r.Context())

	// The pages load nothing but their own stylesheet, and can't be framed.
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'self'; form-action 'self'; frame-ancestors 'none'")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := pages[name].Execute(w, p); err != nil {
		slog.Error("rendering page", "page", name, "err", err)
	}
}

func (s *site) uiOverview(w http.ResponseWriter, r *http.Request) {
	problems := audit.Recent(audit.Filter{Result: audit.Failed, Limit: 15})
	problems = append(problems, audit.Recent(audit.Filter{Result: audit.Denied, Limit: 15})...)
	s.render(w, r, "overview", page{
		Title:    "Overview",
		Refresh:  true,
		Sessions: s.src.Sessions(),
		Events:   newestFirst(problems, 15),
	})
}

func (s *site) uiEvents(w http.ResponseWriter, r *http.Request) {
	f := eventFilter(r)
	s.render(w, r, "events", page{
		Title:      "Events",
		Refresh:    true,
		Events:     audit.Recent(f),
		Filter:     f,
		EventNames: eventNames,
		Results:    []string{audit.OK, audit.Denied, audit.Failed},
	})
}

func (s *site) uiActions(w http.ResponseWriter, r *http.Request) {
	p := page{Title: "Post-upload actions", Refresh: true}
	pending, failed, err := s.src.Actions()
	if err != nil {
		p.Error = err.Error()
	}
	p.Pending, p.Failed = pending, failed
	s.render(w, r, "actions", p)
}

func (s *site) uiConfig(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, "config", page{Title: "Config", Config: string(s.src.Config().Raw)})
}

func serveCSS(w http.ResponseWriter, _ *http.Request) {
	css, _ := files.ReadFile("templates/style.css")
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	_, _ = w.Write(css)
}

// eventNames are the audit event types, for the filter menu.
var eventNames = []string{
	"conn_rejected", "auth_failed", "session_start", "session_end",
	"upload", "download", "delete", "rename", "mkdir", "rmdir",
	"action", "config_reload",
}

// newestFirst merges events and keeps the newest n.
func newestFirst(events []audit.Event, n int) []audit.Event {
	for i := 1; i < len(events); i++ {
		for j := i; j > 0 && events[j].Time.After(events[j-1].Time); j-- {
			events[j], events[j-1] = events[j-1], events[j]
		}
	}
	return events[:min(len(events), n)]
}

// ago says how long ago t was, like "3m ago".
func ago(t time.Time) string {
	d := time.Since(t).Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(d.Hours()/24))
}

// humanBytes writes a byte count like "1.5 MiB".
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
