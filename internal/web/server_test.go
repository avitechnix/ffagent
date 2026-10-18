package web

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/avitechnix/ffagent/internal/agent"
	"github.com/avitechnix/ffagent/internal/approval"
	"github.com/avitechnix/ffagent/internal/push"
)

type fakeBackend struct {
	decided []string
	fail    error
	logos   map[int]string
}

func (f *fakeBackend) TeamLogo(id int) string { return f.logos[id] }

func (f *fakeBackend) Snapshot() agent.Snapshot {
	return agent.Snapshot{
		Status:  agent.AppStatus{Team: "Drunk Pilots", Week: 6, Mode: "approve", Ready: true},
		Pending: []agent.AppItem{{ID: 7, Number: 1, Kind: "ADD", Summary: "Add Backup RB", Status: "pending"}},
	}
}

func (f *fakeBackend) AskAnything(_ context.Context, q string) (approval.QA, error) {
	if f.fail != nil {
		return approval.QA{}, f.fail
	}
	f.decided = append(f.decided, "chat:"+q)
	return approval.QA{Question: q, Answer: "You're projected to win.", Via: "app"}, nil
}

func (f *fakeBackend) Ask(_ context.Context, id int, q string) (approval.QA, error) {
	if f.fail != nil {
		return approval.QA{}, f.fail
	}
	f.decided = append(f.decided, "ask:"+q)
	return approval.QA{Item: 1, Question: q, Answer: "Because Hall is out.", Via: "app"}, nil
}

func (f *fakeBackend) Decide(id int, approve bool) (agent.AppItem, error) {
	if f.fail != nil {
		return agent.AppItem{}, f.fail
	}
	d := "decline"
	if approve {
		d = "approve"
	}
	f.decided = append(f.decided, d)
	return agent.AppItem{ID: id, Status: map[bool]string{true: "approved", false: "declined"}[approve]}, nil
}

const owner = "me@example.com"

func do(h http.Handler, method, path string, hdr map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "https://box.tail1234.ts.net"+path, nil)
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestOnlyTheOwnersTailscaleLoginGetsIn(t *testing.T) {
	h := (&Server{Backend: &fakeBackend{}, Allowed: []string{owner}}).Handler()
	if w := do(h, "GET", "/api/snapshot", nil); w.Code != http.StatusForbidden {
		t.Errorf("no identity: %d", w.Code)
	}
	if w := do(h, "GET", "/", map[string]string{loginHeader: "stranger@example.com"}); w.Code != http.StatusForbidden {
		t.Errorf("someone else: %d", w.Code)
	}
	w := do(h, "GET", "/api/snapshot", map[string]string{loginHeader: "Me@Example.com"})
	if w.Code != http.StatusOK {
		t.Fatalf("owner: %d %s", w.Code, w.Body)
	}
	var s agent.Snapshot
	if err := json.Unmarshal(w.Body.Bytes(), &s); err != nil || s.Status.Team != "Drunk Pilots" || len(s.Pending) != 1 {
		t.Errorf("snapshot: %v %+v", err, s)
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Error("API responses must not be cached")
	}
}

func TestStaticAppIsServedWithStrictHeaders(t *testing.T) {
	h := (&Server{Backend: &fakeBackend{}, Allowed: []string{owner}}).Handler()
	for _, path := range []string{"/", "/app.js", "/app.css", "/manifest.webmanifest", "/icon-180.png"} {
		w := do(h, "GET", path, map[string]string{loginHeader: owner})
		if w.Code != http.StatusOK {
			t.Errorf("%s: %d", path, w.Code)
		}
	}
	w := do(h, "GET", "/", map[string]string{loginHeader: owner})
	csp := w.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "script-src 'self'") || strings.Contains(csp, "unsafe-inline") || !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Errorf("weak CSP: %q", csp)
	}
	if !strings.Contains(w.Body.String(), `rel="apple-touch-icon"`) {
		t.Error("index.html missing the Home Screen icon")
	}
}

func TestDecisionsNeedCSRFProtection(t *testing.T) {
	f := &fakeBackend{}
	h := (&Server{Backend: f, Allowed: []string{owner}}).Handler()
	base := map[string]string{loginHeader: owner}
	if w := do(h, "POST", "/api/moves/7/approve", base); w.Code != http.StatusForbidden {
		t.Errorf("missing CSRF header accepted: %d", w.Code)
	}
	cross := map[string]string{loginHeader: owner, csrfHeader: "1", "Origin": "https://evil.example"}
	if w := do(h, "POST", "/api/moves/7/approve", cross); w.Code != http.StatusForbidden {
		t.Errorf("cross-origin accepted: %d", w.Code)
	}
	ok := map[string]string{loginHeader: owner, csrfHeader: "1", "Origin": "https://box.tail1234.ts.net"}
	if w := do(h, "POST", "/api/moves/7/approve", ok); w.Code != http.StatusOK {
		t.Fatalf("approve: %d %s", w.Code, w.Body)
	}
	if w := do(h, "POST", "/api/moves/7/decline", ok); w.Code != http.StatusOK {
		t.Fatalf("decline: %d", w.Code)
	}
	if w := do(h, "POST", "/api/moves/7/delete", ok); w.Code != http.StatusNotFound {
		t.Errorf("unknown decision: %d", w.Code)
	}
	if w := do(h, "GET", "/api/moves/7/approve", ok); w.Code == http.StatusOK && len(f.decided) > 2 {
		t.Error("GET must not change anything")
	}
	if strings.Join(f.decided, ",") != "approve,decline" {
		t.Errorf("decisions: %v", f.decided)
	}
	f.fail = errors.New("that move was already executed")
	if w := do(h, "POST", "/api/moves/7/approve", ok); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "already executed") {
		t.Errorf("conflict: %d %s", w.Code, w.Body)
	}
}

func TestAskEndpoint(t *testing.T) {
	f := &fakeBackend{}
	h := (&Server{Backend: f, Allowed: []string{owner}}).Handler()
	post := func(body string, hdr map[string]string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "https://box.tail1234.ts.net/api/moves/7/ask", strings.NewReader(body))
		for k, v := range hdr {
			r.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	ok := map[string]string{loginHeader: owner, csrfHeader: "1"}
	if w := post(`{"question":"why Davis?"}`, map[string]string{loginHeader: owner}); w.Code != http.StatusForbidden {
		t.Errorf("ask without CSRF header: %d", w.Code)
	}
	w := post(`{"question":"why Davis?"}`, ok)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Hall is out") {
		t.Fatalf("ask: %d %s", w.Code, w.Body)
	}
	if f.decided[0] != "ask:why Davis?" {
		t.Errorf("backend got %v", f.decided)
	}
	if w := post(`not json`, ok); w.Code != http.StatusBadRequest {
		t.Errorf("bad body: %d", w.Code)
	}
	if w := post(`{"question":"`+strings.Repeat("a", 9000)+`"}`, ok); w.Code != http.StatusBadRequest {
		t.Errorf("oversized body: %d", w.Code)
	}
}

func TestAskAnythingEndpoint(t *testing.T) {
	f := &fakeBackend{}
	h := (&Server{Backend: f, Allowed: []string{owner}}).Handler()
	post := func(body string, hdr map[string]string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "https://box.tail1234.ts.net/api/ask", strings.NewReader(body))
		for k, v := range hdr {
			r.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := post(`{"question":"am I winning?"}`, map[string]string{loginHeader: owner}); w.Code != http.StatusForbidden {
		t.Errorf("without CSRF header: %d", w.Code)
	}
	ok := map[string]string{loginHeader: owner, csrfHeader: "1"}
	if w := post(`{"question":"am I winning?"}`, ok); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "projected to win") {
		t.Fatalf("ask: %d %s", w.Code, w.Body)
	}
	if f.decided[0] != "chat:am I winning?" {
		t.Errorf("backend got %v", f.decided)
	}
	if w := post(`{"question":"`+strings.Repeat("a", 9000)+`"}`, ok); w.Code != http.StatusBadRequest {
		t.Errorf("oversized body: %d", w.Code)
	}
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestPushEndpoints(t *testing.T) {
	off := (&Server{Backend: &fakeBackend{}, Allowed: []string{owner}}).Handler()
	if rr := do(off, "GET", "/api/push", map[string]string{loginHeader: owner}); !strings.Contains(rr.Body.String(), `"enabled":false`) {
		t.Errorf("push should be off without a store: %s", rr.Body)
	}

	store, err := push.Open(filepath.Join(t.TempDir(), "push.vault"), []byte("pass"), "mailto:bot@example.com")
	if err != nil {
		t.Fatal(err)
	}
	sent := make(chan string, 4)
	store.Client = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		sent <- r.URL.String()
		return &http.Response{StatusCode: http.StatusCreated, Body: http.NoBody, Request: r}, nil
	})}
	h := (&Server{Backend: &fakeBackend{}, Allowed: []string{owner}, Push: store}).Handler()

	rr := do(h, "GET", "/api/push", map[string]string{loginHeader: owner})
	var info struct {
		Enabled bool   `json:"enabled"`
		Key     string `json:"key"`
	}
	json.Unmarshal(rr.Body.Bytes(), &info)
	if !info.Enabled || info.Key != store.PublicKey() {
		t.Fatalf("key: %s", rr.Body)
	}

	k, _ := ecdh.P256().GenerateKey(rand.Reader)
	sub := fmt.Sprintf(`{"endpoint":"https://web.push.apple.com/abc","keys":{"p256dh":%q,"auth":"BTBZMqHH6r4Tts7J_aSIgg"}}`,
		base64.RawURLEncoding.EncodeToString(k.PublicKey().Bytes()))
	post := func(path, body string, hdr map[string]string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "https://box.tail1234.ts.net"+path, strings.NewReader(body))
		for k, v := range hdr {
			r.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	ok := map[string]string{loginHeader: owner, csrfHeader: "1"}
	if rr := post("/api/push/subscribe", sub, map[string]string{loginHeader: owner}); rr.Code != http.StatusForbidden {
		t.Errorf("subscribe without the CSRF header: %d", rr.Code)
	}
	if rr := post("/api/push/subscribe", sub, map[string]string{csrfHeader: "1"}); rr.Code != http.StatusForbidden {
		t.Errorf("subscribe without a Tailscale login: %d", rr.Code)
	}
	if rr := post("/api/push/subscribe", strings.Replace(sub, "web.push.apple.com", "evil.example.com", 1), ok); rr.Code != http.StatusUnprocessableEntity {
		t.Errorf("subscribed an endpoint that isn't a push service: %d", rr.Code)
	}
	if rr := post("/api/push/subscribe", sub, ok); rr.Code != http.StatusOK || !store.Has("https://web.push.apple.com/abc") {
		t.Fatalf("subscribe: %d %s", rr.Code, rr.Body)
	}
	select {
	case u := <-sent:
		if u != "https://web.push.apple.com/abc" {
			t.Errorf("welcome sent to %s", u)
		}
	case <-time.After(5 * time.Second):
		t.Error("no welcome notification")
	}
	if rr := post("/api/push/unsubscribe", sub, ok); rr.Code != http.StatusOK || store.Count() != 0 {
		t.Errorf("unsubscribe: %d, %d left", rr.Code, store.Count())
	}
}
