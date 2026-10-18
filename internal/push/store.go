package push

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/avitechnix/ffagent/internal/secrets"
)

// MaxSubscriptions caps how many devices can subscribe.
const MaxSubscriptions = 5

// Notification is one message to the phone.
type Notification struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	URL   string `json:"url,omitempty"` // in-app path to open, e.g. "./#moves"
	Tag   string `json:"tag,omitempty"` // replaces an earlier notification with the same tag
}

// Store holds the VAPID key and the device subscriptions, sealed in an
// encrypted file with the vault passphrase.
type Store struct {
	path   string
	pass   []byte
	Client *http.Client

	mu    sync.Mutex
	vapid *VAPID
	subs  []Subscription
}

// Open reads the store at path, creating it (with a new VAPID key) if it
// doesn't exist. subject is the contact push services see (mailto: or https:).
func Open(path string, passphrase []byte, subject string) (*Store, error) {
	s := &Store{path: path, pass: append([]byte(nil), passphrase...), Client: &http.Client{Timeout: 15 * time.Second}}
	vals, err := secrets.ReadVault(path, s.pass)
	if err != nil {
		if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
			return nil, err
		}
		d, err := NewVAPIDKey()
		if err != nil {
			return nil, err
		}
		if s.vapid, err = ParseVAPID(d, subject); err != nil {
			return nil, err
		}
		return s, s.save()
	}
	d, err := base64.StdEncoding.DecodeString(vals["vapid_key"].Reveal())
	if err != nil {
		return nil, fmt.Errorf("push: bad key in %s", path)
	}
	if s.vapid, err = ParseVAPID(d, subject); err != nil {
		return nil, err
	}
	if raw := vals["subscriptions"].Reveal(); raw != "" {
		if err := json.Unmarshal([]byte(raw), &s.subs); err != nil {
			return nil, fmt.Errorf("push: bad subscriptions in %s", path)
		}
	}
	return s, nil
}

// save seals the store. Call with s.mu held (or before it's shared).
func (s *Store) save() error {
	subs, err := json.Marshal(s.subs)
	if err != nil {
		return err
	}
	data, err := secrets.Seal(s.pass, map[string]secrets.Secret{
		"vapid_key":     secrets.New(base64.StdEncoding.EncodeToString(s.vapid.key.D.FillBytes(make([]byte, 32)))),
		"subscriptions": secrets.New(string(subs)),
	})
	if err != nil {
		return err
	}
	return secrets.WriteVault(s.path, data)
}

// PublicKey is what the app subscribes with.
func (s *Store) PublicKey() string { return s.vapid.PublicKey() }

// Count is how many devices are subscribed.
func (s *Store) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.subs)
}

// Has reports whether this endpoint is subscribed.
func (s *Store) Has(endpoint string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, x := range s.subs {
		if x.Endpoint == endpoint {
			return true
		}
	}
	return false
}

// Add stores a subscription (replacing one with the same endpoint).
func (s *Store) Add(sub Subscription) error {
	if err := sub.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := []Subscription{sub}
	for _, x := range s.subs {
		if x.Endpoint != sub.Endpoint {
			kept = append(kept, x)
		}
	}
	if len(kept) > MaxSubscriptions {
		kept = kept[:MaxSubscriptions] // the newest stays; the oldest go
	}
	s.subs = kept
	return s.save()
}

// Remove forgets a subscription.
func (s *Store) Remove(endpoint string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := s.subs[:0]
	for _, x := range s.subs {
		if x.Endpoint != endpoint {
			kept = append(kept, x)
		}
	}
	s.subs = kept
	return s.save()
}

// Notify sends n to every subscribed device, forgetting ones that are gone.
// It returns how many devices it reached.
func (s *Store) Notify(ctx context.Context, n Notification) (int, error) {
	payload, err := json.Marshal(n)
	if err != nil {
		return 0, err
	}
	s.mu.Lock()
	subs := append([]Subscription(nil), s.subs...)
	s.mu.Unlock()
	sent := 0
	var gone []string
	var errs []error
	for _, sub := range subs {
		switch err := Send(ctx, s.Client, s.vapid, sub, payload, 24*time.Hour); {
		case err == nil:
			sent++
		case errors.Is(err, ErrGone):
			gone = append(gone, sub.Endpoint)
		default:
			errs = append(errs, err)
		}
	}
	for _, ep := range gone {
		_ = s.Remove(ep)
	}
	return sent, errors.Join(errs...)
}
