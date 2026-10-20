package secrets

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
)

// On macOS the vault passphrase can live in the login Keychain instead of a
// systemd credential: set FFAGENT_KEYCHAIN to the item's service name (the
// launchd service and ffagentctl set it to "ffagent"). The passphrase is
// read with Apple's `security` tool, which the item allows without a prompt.

// keychainAccount is the account name of the Keychain item.
const keychainAccount = "vault-passphrase"

// securityTool is Apple's Keychain command-line tool (a variable for tests).
var securityTool = "/usr/bin/security"

var keychainServicePattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// ErrNotInKeychain means the Keychain has no passphrase item yet.
var ErrNotInKeychain = errors.New("secrets: no vault passphrase in the Keychain")

func checkService(service string) error {
	if !keychainServicePattern.MatchString(service) {
		return fmt.Errorf("secrets: bad Keychain service name %q", service)
	}
	return nil
}

// keychainRead returns the passphrase stored under service.
func keychainRead(service string) ([]byte, error) {
	if err := checkService(service); err != nil {
		return nil, err
	}
	var out, errOut bytes.Buffer
	cmd := exec.Command(securityTool, "find-generic-password", "-s", service, "-a", keychainAccount, "-w")
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 44 { // errSecItemNotFound
			return nil, ErrNotInKeychain
		}
		msg := strings.TrimSpace(errOut.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("secrets: reading the Keychain failed (is it unlocked? the Mac must be logged in): %s", msg)
	}
	p := bytes.TrimRight(out.Bytes(), "\r\n")
	if len(p) == 0 {
		return nil, errors.New("secrets: the Keychain passphrase is empty")
	}
	return p, nil
}

// KeychainInit makes a random vault passphrase and stores it in the
// Keychain under service, unless one is already there. The passphrase is
// handed to `security` on its standard input, never on a command line,
// and is never shown. It reports whether a new one was created.
func KeychainInit(service string) (bool, error) {
	if p, err := keychainRead(service); err == nil {
		wipe(p)
		return false, nil
	} else if !errors.Is(err, ErrNotInKeychain) {
		return false, err
	}
	raw := make([]byte, 36)
	if _, err := rand.Read(raw); err != nil {
		return false, err
	}
	pass := []byte(base64.RawURLEncoding.EncodeToString(raw)) // no quotes or spaces for security's parser
	wipe(raw)
	defer wipe(pass)
	// -T allows the security tool itself to read the item without a
	// prompt, so the background service can start unattended.
	line := []byte(fmt.Sprintf("add-generic-password -s %s -a %s -l %s-vault -T %s -w ", service, keychainAccount, service, securityTool))
	line = append(append(line, pass...), '\n')
	defer wipe(line)
	var errOut bytes.Buffer
	cmd := exec.Command(securityTool, "-i")
	cmd.Stdin, cmd.Stderr = bytes.NewReader(line), &errOut
	if err := cmd.Run(); err != nil {
		return false, fmt.Errorf("secrets: storing the passphrase in the Keychain failed: %s", strings.TrimSpace(errOut.String()+" "+err.Error()))
	}
	// security -i reports a failed command on stderr but still exits 0.
	if msg := strings.TrimSpace(errOut.String()); msg != "" && !strings.HasPrefix(msg, "security>") {
		return false, fmt.Errorf("secrets: storing the passphrase in the Keychain failed: %s", msg)
	}
	check, err := keychainRead(service)
	if err != nil {
		return false, err
	}
	defer wipe(check)
	if !bytes.Equal(check, pass) {
		return false, errors.New("secrets: the Keychain didn't keep the passphrase")
	}
	return true, nil
}
