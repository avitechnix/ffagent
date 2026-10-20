package secrets

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeSecurity stands in for /usr/bin/security: it keeps one item in a
// file, answers find-generic-password -w like the real tool (exit 44 when
// missing), and reads add-generic-password from `security -i` on stdin,
// logging every command line it was started with.
func fakeSecurity(t *testing.T) (store, argLog string) {
	t.Helper()
	dir := t.TempDir()
	store, argLog = filepath.Join(dir, "item"), filepath.Join(dir, "args")
	script := `#!/bin/sh
echo "$@" >> '` + argLog + `'
case "$1" in
find-generic-password)
  [ -f '` + store + `' ] || { echo "security: SecKeychainSearchCopyNext: The specified item could not be found in the keychain." >&2; exit 44; }
  cat '` + store + `'; echo; exit 0 ;;
-i)
  read -r line
  case "$line" in
  "add-generic-password -s ffagent -a vault-passphrase -l ffagent-vault -T "*" -w "*) printf '%s' "${line##* -w }" > '` + store + `' ;;
  *) echo "unexpected: $line" >&2 ;;
  esac
  exit 0 ;;
esac
exit 1
`
	bin := filepath.Join(dir, "security")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	old := securityTool
	securityTool = bin
	t.Cleanup(func() { securityTool = old })
	return store, argLog
}

func TestKeychainPassphrase(t *testing.T) {
	store, argLog := fakeSecurity(t)
	t.Setenv("CREDENTIALS_DIRECTORY", "")
	t.Setenv("FFAGENT_KEYCHAIN", "ffagent")

	if _, err := Passphrase(); err != ErrNotInKeychain {
		t.Fatalf("before init: %v", err)
	}
	created, err := KeychainInit("ffagent")
	if err != nil || !created {
		t.Fatalf("init: %v %v", created, err)
	}
	stored, _ := os.ReadFile(store)
	if len(stored) < 40 || strings.ContainsAny(string(stored), " \"'\n") {
		t.Fatalf("stored passphrase %q", stored)
	}
	args, _ := os.ReadFile(argLog)
	if strings.Contains(string(args), string(stored)) {
		t.Error("the passphrase was passed on a command line")
	}

	p, ok, err := Unattended()
	if err != nil || !ok || string(p) != string(stored) {
		t.Fatalf("unattended: %q %v %v", p, ok, err)
	}
	if created, err := KeychainInit("ffagent"); err != nil || created {
		t.Errorf("second init replaced the passphrase: %v %v", created, err)
	}
	if again, _ := os.ReadFile(store); string(again) != string(stored) {
		t.Error("passphrase changed")
	}
	if _, err := KeychainInit("bad name; rm -rf"); err == nil {
		t.Error("accepted a bad service name")
	}
}
