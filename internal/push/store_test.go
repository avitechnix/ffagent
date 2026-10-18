package push

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func testSub(t *testing.T, endpoint string) Subscription {
	t.Helper()
	k, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	auth := make([]byte, 16)
	rand.Read(auth)
	var s Subscription
	s.Endpoint = endpoint
	s.Keys.P256DH = b64.EncodeToString(k.PublicKey().Bytes())
	s.Keys.Auth = b64.EncodeToString(auth)
	return s
}

func TestStoreKeepsKeyAndSubscriptionsEncrypted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "push.vault")
	pass := []byte("correct horse battery staple")
	s, err := Open(path, pass, "mailto:bot@example.com")
	if err != nil {
		t.Fatal(err)
	}
	key := s.PublicKey()
	for i := range MaxSubscriptions + 2 {
		if err := s.Add(testSub(t, "https://web.push.apple.com/dev"+string(rune('a'+i)))); err != nil {
			t.Fatal(err)
		}
	}
	again := testSub(t, "https://web.push.apple.com/deve") // re-subscribing replaces, not duplicates
	if err := s.Add(again); err != nil {
		t.Fatal(err)
	}
	if s.Count() != MaxSubscriptions || !s.Has(again.Endpoint) || s.Has("https://web.push.apple.com/deva") {
		t.Fatalf("count %d", s.Count())
	}
	if err := s.Add(testSub(t, "https://evil.example.com/x")); err == nil {
		t.Error("accepted an endpoint that isn't a push service")
	}

	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), "web.push.apple.com") || strings.Contains(string(raw), again.Keys.Auth) {
		t.Error("subscriptions are readable on disk")
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm()&0o077 != 0 {
		t.Errorf("store is %v", fi.Mode().Perm())
	}

	s2, err := Open(path, pass, "mailto:bot@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if s2.PublicKey() != key || s2.Count() != MaxSubscriptions || !s2.Has(again.Endpoint) {
		t.Error("store didn't survive reopening")
	}
	if _, err := Open(path, []byte("wrong"), ""); err == nil {
		t.Error("opened with the wrong passphrase")
	}
	if err := s2.Remove(again.Endpoint); err != nil || s2.Has(again.Endpoint) {
		t.Error("remove failed")
	}
}

func TestNotifySendsAndForgetsExpired(t *testing.T) {
	var mu sync.Mutex
	var got []*http.Request
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got = append(got, r)
		mu.Unlock()
		if r.URL.Path == "/gone" {
			w.WriteHeader(http.StatusGone)
			return
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()
	pushHosts = append(pushHosts, "127.0.0.1")
	defer func() { pushHosts = pushHosts[:len(pushHosts)-1] }()

	s, err := Open(filepath.Join(t.TempDir(), "push.vault"), []byte("pass"), "mailto:bot@example.com")
	if err != nil {
		t.Fatal(err)
	}
	s.Client = srv.Client()
	if err := s.Add(testSub(t, srv.URL+"/gone")); err != nil {
		t.Fatal(err)
	}
	if err := s.Add(testSub(t, srv.URL+"/ok")); err != nil {
		t.Fatal(err)
	}
	n, err := s.Notify(context.Background(), Notification{Title: "Lineup", Body: "Swap in Pollard", URL: "./#moves"})
	if err != nil || n != 1 {
		t.Fatalf("sent %d, %v", n, err)
	}
	if s.Count() != 1 || s.Has(srv.URL+"/gone") {
		t.Error("expired subscription wasn't forgotten")
	}
	for _, r := range got {
		if r.Header.Get("Content-Encoding") != "aes128gcm" || !strings.HasPrefix(r.Header.Get("Authorization"), "vapid t=") || r.Header.Get("TTL") == "" {
			t.Errorf("headers %v", r.Header)
		}
	}
}
