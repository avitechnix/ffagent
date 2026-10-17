package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRejectsPlaintextSecretsInFile(t *testing.T) {
	t.Setenv("ESPN_LEAGUE_ID", "1")
	for _, key := range []string{"espn_s2", "SWID"} {
		p := filepath.Join(t.TempDir(), "c.json")
		os.WriteFile(p, []byte(fmt.Sprintf(`{"league_id": 1, %q: "AEBplaintext"}`, key)), 0o600)
		_, err := Load(p)
		if !errors.Is(err, ErrPlaintextSecret) {
			t.Errorf("%s in config: err = %v", key, err)
		}
		if err != nil && strings.Contains(err.Error(), "AEBplaintext") {
			t.Errorf("error message leaks the value: %v", err)
		}
	}
}

func TestRejectsPlaintextSecretsInEnv(t *testing.T) {
	t.Setenv("ESPN_LEAGUE_ID", "1")
	t.Setenv("ESPN_S2", "AEBplaintext")
	_, err := Load("")
	if !errors.Is(err, ErrPlaintextSecret) {
		t.Fatalf("err = %v", err)
	}
	if _, ok := os.LookupEnv("ESPN_S2"); ok {
		t.Error("plaintext env var should be scrubbed from the environment")
	}
}

func TestExampleConfigIsAccepted(t *testing.T) {
	if _, err := Load("../../config.example.json"); err != nil {
		t.Fatal(err)
	}
}

func TestRejectsNestedEmailPassword(t *testing.T) {
	t.Setenv("ESPN_LEAGUE_ID", "1")
	p := filepath.Join(t.TempDir(), "c.json")
	os.WriteFile(p, []byte(`{"league_id": 1, "email": {"address": "a@b.c", "app_password": "abcd efgh"}}`), 0o600)
	if _, err := Load(p); !errors.Is(err, ErrPlaintextSecret) {
		t.Fatalf("nested password accepted: %v", err)
	}
}

func TestWebAppOnlyListensOnLocalhost(t *testing.T) {
	t.Setenv("ESPN_LEAGUE_ID", "1")
	write := func(web string) string {
		p := filepath.Join(t.TempDir(), "c.json")
		os.WriteFile(p, []byte(`{"league_id": 1, "web": `+web+`}`), 0o600)
		return p
	}
	if _, err := Load(write(`{"enabled": true, "listen": "0.0.0.0:8787", "allowed_logins": ["me@x.com"]}`)); err == nil {
		t.Error("public listen address accepted")
	}
	if _, err := Load(write(`{"enabled": true, "listen": "127.0.0.1:8787"}`)); err == nil {
		t.Error("enabled without allowed_logins accepted")
	}
	c, err := Load(write(`{"enabled": true, "listen": "127.0.0.1:8787", "allowed_logins": [" Me@X.com "]}`))
	if err != nil || c.Web.AllowedLogins[0] != "me@x.com" {
		t.Errorf("valid web config: %v %+v", err, c.Web)
	}
}
