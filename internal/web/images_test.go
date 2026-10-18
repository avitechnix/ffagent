package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
)

func TestImageProxy(t *testing.T) {
	var hits atomic.Int32
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		img := r.URL.Query().Get("img")
		switch {
		case img == "/i/headshots/nfl/players/full/4241389.png":
			w.Header().Set("Content-Type", "image/png")
			io.WriteString(w, "\x89PNG fake")
		case img == "/i/headshots/nfl/players/full/77.png":
			w.Header().Set("Content-Type", "text/html")
			io.WriteString(w, "<script>alert(1)</script>")
		default:
			http.NotFound(w, r)
		}
	}))
	defer cdn.Close()

	srv := &Server{Backend: &fakeBackend{}, Allowed: []string{"me@example.com"}, Images: &Images{Base: cdn.URL}}
	h := srv.Handler()
	get := func(path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set(loginHeader, "me@example.com")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}

	w := get("/img/p/4241389.png")
	if w.Code != 200 || w.Header().Get("Content-Type") != "image/png" || !strings.HasPrefix(w.Body.String(), "\x89PNG") {
		t.Fatalf("headshot: %d %q", w.Code, w.Header().Get("Content-Type"))
	}
	if !strings.Contains(w.Header().Get("Cache-Control"), "max-age") || w.Header().Get("Content-Security-Policy") == "" {
		t.Errorf("headers: %v", w.Header())
	}
	get("/img/p/4241389.png")
	if hits.Load() != 1 {
		t.Errorf("second request wasn't cached: %d CDN hits", hits.Load())
	}
	if w := get("/img/p/999.png"); w.Code != 404 {
		t.Errorf("missing headshot: %d", w.Code)
	}
	if w := get("/img/p/77.png"); w.Code != 502 {
		t.Errorf("non-image served: %d", w.Code)
	}
	// Only player IDs: nothing else reaches the CDN, NFL team logos included.
	before := hits.Load()
	for _, p := range []string{"/img/p/-16030.png", "/img/p/..%2fetc.png", "/img/t/jax.png", "/img/x/1.png", "/img/t/toolong.png"} {
		if w := get(p); w.Code != 404 {
			t.Errorf("%s: %d, want 404", p, w.Code)
		}
	}
	if hits.Load() != before {
		t.Errorf("rejected paths reached the CDN")
	}
	// The usual login check applies.
	r := httptest.NewRequest("GET", "/img/p/4241389.png", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusForbidden {
		t.Errorf("no login: %d", rec.Code)
	}
}

func TestFantasyTeamLogos(t *testing.T) {
	logo := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg; charset=binary")
		io.WriteString(w, "JPEG fake")
	}))
	defer logo.Close()
	be := &fakeBackend{logos: map[int]string{
		1: logo.URL + "/drunk-pilots.jpg",
		2: "http://example.com/plain-http.png",
		3: "https://user:pw@example.com/x.png",
	}}
	get := func(srv *Server, path string) int {
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set(loginHeader, "me@example.com")
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, r)
		return w.Code
	}

	// With a client that trusts the test server, the logo is proxied.
	ok := &Server{Backend: be, Allowed: []string{"me@example.com"}, Images: &Images{LogoClient: logo.Client()}}
	if code := get(ok, "/img/f/1.png"); code != 200 {
		t.Errorf("team logo: %d", code)
	}
	for _, p := range []string{"/img/f/2.png", "/img/f/3.png", "/img/f/9.png", "/img/f/abc.png", "/img/f/-1.png"} {
		if code := get(ok, p); code != 404 {
			t.Errorf("%s: %d, want 404", p, code)
		}
	}

	// The default client refuses non-public addresses, like this test
	// server on 127.0.0.1, so a logo URL can't reach the agent's network.
	def := &Server{Backend: be, Allowed: []string{"me@example.com"}}
	if code := get(def, "/img/f/1.png"); code != 502 {
		t.Errorf("logo on a loopback address: %d, want 502", code)
	}
}

func TestPublicAddr(t *testing.T) {
	for addr, want := range map[string]bool{
		"8.8.8.8": true, "2606:4700::1111": true,
		"127.0.0.1": false, "10.1.2.3": false, "192.168.1.5": false, "172.16.0.1": false,
		"169.254.169.254": false, "100.101.102.103": false, "::1": false, "fd7a:115c:a1e0::1": false, "0.0.0.0": false,
		"::ffff:127.0.0.1": false,
	} {
		if got := publicAddr(netip.MustParseAddr(addr)); got != want {
			t.Errorf("%s: %v, want %v", addr, got, want)
		}
	}
}
