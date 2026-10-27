// Package web serves the local interface of the sizer, its JSON API and its health check.
package web

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/Hytm/aerospike-sizer/internal/buildinfo"
	"github.com/Hytm/aerospike-sizer/internal/catalog"
	"github.com/Hytm/aerospike-sizer/internal/plan"
	"github.com/Hytm/aerospike-sizer/internal/pricing"
)

//go:embed static
var staticFS embed.FS

//go:embed index.html
var indexHTML string

var indexTemplate = template.Must(template.New("index").Parse(indexHTML))

// assetsVersion changes with the embedded assets: browsers cache them for an hour, and a rebuild
// must never leave a stale script behind (the version of a dev build does not change).
var assetsVersion = func() string {
	h := sha256.New()
	fs.WalkDir(staticFS, "static", func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			b, _ := staticFS.ReadFile(path)
			h.Write(b)
		}
		return nil
	})
	return hex.EncodeToString(h.Sum(nil))[:12]
}()

// Server holds what the handlers need.
type Server struct {
	Log    *slog.Logger
	Slot   string        // launchd instance (a or b), empty when run by hand
	Prices *pricing.Book // a book without fetchers quotes reference prices only
}

// Handler returns the HTTP routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	static, _ := fs.Sub(staticFS, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", cacheStatic(http.FileServerFS(static))))
	mux.HandleFunc("GET /health", s.health)
	mux.HandleFunc("GET /{$}", s.index)
	mux.HandleFunc("GET /api/defaults", s.defaults)
	mux.HandleFunc("POST /api/plan", s.plan)
	mux.HandleFunc("POST /api/refresh", s.refresh)
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
		// The interface asks for a plan on every keystroke: only failures are worth a line.
		quiet := strings.HasPrefix(r.URL.Path, "/static/") || r.URL.Path == "/health" || strings.HasPrefix(r.URL.Path, "/api/")
		if !quiet || rec.code >= 400 {
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

func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	if err := indexTemplate.Execute(w, map[string]string{"Version": buildinfo.Version, "Assets": assetsVersion}); err != nil {
		s.Log.Error("render index", "err", err)
	}
}

// defaults gives the interface its catalog and the form as it opens.
func (s *Server) defaults(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{
		"request":       plan.DefaultRequest(),
		"providers":     catalog.Providers,
		"prices_as_of":  catalog.PricesAsOf,
		"prices_region": catalog.PricesRegion,
		"version":       buildinfo.Version,
	})
}

func (s *Server) plan(w http.ResponseWriter, r *http.Request) {
	req := plan.DefaultRequest() // fields the request leaves out keep their default
	if !s.decode(w, r, &req) {
		return
	}
	writeJSON(w, plan.Compute(req, s.Prices))
}

// refresh drops a cached price so that the next plan looks it up again.
func (s *Server) refresh(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Provider string `json:"provider"`
		Region   string `json:"region"`
		Instance string `json:"instance"`
	}
	if !s.decode(w, r, &req) {
		return
	}
	s.Prices.Forget(req.Provider, req.Region, req.Instance)
	w.WriteHeader(http.StatusNoContent)
}

// maxRequest bounds a JSON request.
const maxRequest = 1 << 20

// decode reads a JSON body. Requiring the JSON content type keeps other sites from posting to the
// API: their browsers must ask first, and the server never allows them.
func (s *Server) decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt != "application/json" {
		http.Error(w, "expected application/json", http.StatusUnsupportedMediaType)
		return false
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequest)).Decode(v); err != nil {
		http.Error(w, "invalid request: "+err.Error(), http.StatusBadRequest)
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(v)
}
