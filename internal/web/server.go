// Package web serves the local interface of the sizer and its health check.
package web

import (
	"embed"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/Hytm/aerospike-sizer/internal/buildinfo"
)

//go:embed static
var staticFS embed.FS

// Server holds what the handlers need.
type Server struct {
	Log  *slog.Logger
	Slot string // launchd instance (a or b), empty when run by hand
}

// Handler returns the HTTP routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	static, _ := fs.Sub(staticFS, "static")
	files := http.FileServerFS(static)
	mux.Handle("GET /static/", http.StripPrefix("/static/", cacheStatic(files)))
	mux.HandleFunc("GET /health", s.health)
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeFileFS(w, r, static, "index.html")
	})
	return s.logRequests(mux)
}

func cacheStatic(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=3600")
		h.ServeHTTP(w, r)
	})
}

func (s *Server) logRequests(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, code: http.StatusOK}
		h.ServeHTTP(rec, r)
		if !strings.HasPrefix(r.URL.Path, "/static/") && r.URL.Path != "/health" {
			s.Log.Info("http", "method", r.Method, "path", r.URL.Path, "status", rec.code, "duration", time.Since(start).Round(time.Millisecond))
		}
	})
}

type statusRecorder struct {
	http.ResponseWriter
	code int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.code = code
	r.ResponseWriter.WriteHeader(code)
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"status":"ok","pid":%d,"version":%q,"slot":%q}`+"\n", os.Getpid(), buildinfo.Version, s.Slot)
}
