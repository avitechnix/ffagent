package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigSetKeepsOrderAndValidates(t *testing.T) {
	example, err := os.ReadFile("../../config.example.json")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.json")
	os.WriteFile(path, example, 0o600)

	for _, kv := range [][2]string{{"league_id", "12345"}, {"web.enabled", "true"}, {"web.allowed_logins", `["me@example.com"]`}, {"email.time_zone", "America/Chicago"}, {"ask", `{"enabled": true, "max_per_day": 10}`}} {
		if err := configSet(path, kv[0], kv[1]); err != nil {
			t.Fatalf("%s: %v", kv[0], err)
		}
	}
	b, _ := os.ReadFile(path)
	s := string(b)
	for _, want := range []string{`"league_id": 12345`, `"allowed_logins": ["me@example.com"]`, `"time_zone": "America/Chicago"`, `"max_per_day": 10`} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %s in\n%s", want, s)
		}
	}
	if strings.Index(s, `"league_id"`) > strings.Index(s, `"email"`) {
		t.Error("key order changed")
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", fi.Mode().Perm())
	}

	// A value the agent rejects leaves the file untouched.
	if err := configSet(path, "web.listen", "0.0.0.0:80"); err == nil {
		t.Error("accepted a public listen address")
	}
	if after, _ := os.ReadFile(path); string(after) != s {
		t.Error("a rejected change was written")
	}
}
