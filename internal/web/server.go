// Package web serves the phone app: a small single-page web app you add to
// your iPhone's Home Screen. It listens on localhost; `tailscale serve`
// publishes it to your tailnet with HTTPS and adds the identity headers
// this server checks, so only your Tailscale login can use it.
package web

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/avitechnix/ffagent/internal/agent"
	"github.com/avitechnix/ffagent/internal/approval"
)

//go:embed static
var static embed.FS

// Backend is what the app needs from the watcher.
type Backend interface {
	Snapshot() agent.Snapshot
	Decide(id int, approve bool) (agent.AppItem, error)
	Ask(ctx context.Context, id int, question string) (approval.QA, error)
	// TeamLogo returns the logo URL a fantasy team in the league chose.
	TeamLogo(id int) string
}

// Server is the phone app's HTTP server.
type Server struct {
	Backend Backend
	// Allowed lists Tailscale logins (lower-case) that may use the app.
	Allowed []string
	// Images proxies player photos and team logos (a default when nil).
	Images *Images
}

// loginHeader is set by `tailscale serve` for requests from tailnet users;
// Tailscale strips any copy a client tries to send itself.
const loginHeader = "Tailscale-User-Login"

// csrfHeader must accompany state-changing requests. Browsers can't add
// custom headers to cross-site requests without a CORS preflight, which
// this server never approves.
const csrfHeader = "X-FFAgent-Request"

// Handler returns the app's routes wrapped in authentication.
func (s *Server) Handler() http.Handler {
	files, _ := fs.Sub(static, "static")
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/snapshot", s.snapshot)
	mux.HandleFunc("POST /api/moves/{id}/ask", s.ask)
	mux.HandleFunc("POST /api/moves/{id}/{decision}", s.decide)
	if s.Images == nil {
		s.Images = &Images{}
	}
	if s.Images.TeamLogo == nil {
		s.Images.TeamLogo = s.Backend.TeamLogo
	}
	mux.Handle("GET /img/{kind}/{key}", s.Images)
	mux.Handle("GET /", http.FileServerFS(files))
	return s.secure(mux)
}

func (s *Server) secure(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")

		login := strings.ToLower(strings.TrimSpace(r.Header.Get(loginHeader)))
		if !s.allowed(login) {
			msg := "This app is only available to its owner over Tailscale."
			if login != "" {
				msg = "Your Tailscale login (" + login + ") isn't allowed to use this app."
			}
			http.Error(w, msg, http.StatusForbidden)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if r.Header.Get(csrfHeader) != "1" || !sameOrigin(r) {
				http.Error(w, "cross-site request refused", http.StatusForbidden)
				return
			}
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			h.Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) allowed(login string) bool {
	if login == "" {
		return false
	}
	for _, a := range s.Allowed {
		if a == login {
			return true
		}
	}
	return false
}

// sameOrigin accepts requests whose Origin (if any) matches the host.
func sameOrigin(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return true // same-origin fetches from Safari may omit it; the CSRF header still applies
	}
	u, err := url.Parse(o)
	return err == nil && strings.EqualFold(u.Host, r.Host)
}

func (s *Server) snapshot(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.Backend.Snapshot())
}

func (s *Server) decide(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad move id"})
		return
	}
	var approve bool
	switch r.PathValue("decision") {
	case "approve":
		approve = true
	case "decline":
	default:
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown decision"})
		return
	}
	it, err := s.Backend.Decide(id, approve)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, it)
}

func (s *Server) ask(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad move id"})
		return
	}
	var body struct {
		Question string `json:"question"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 8<<10)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "send {\"question\": \"...\"}"})
		return
	}
	qa, err := s.Backend.Ask(r.Context(), id, body.Question)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, qa)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// ListenAndServe serves until ctx is cancelled.
func (s *Server) ListenAndServe(ctx context.Context, addr string) error {
	srv := &http.Server{
		Addr:              addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      150 * time.Second, // answering a question can take a while
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    64 << 10,
	}
	go func() {
		<-ctx.Done()
		shut, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shut)
	}()
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
