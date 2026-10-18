// Package push sends Web Push notifications to the phone app (a Home
// Screen web app, which iOS 16.4+ lets receive pushes). Messages are
// encrypted for the subscribing device (RFC 8291) and signed with the
// agent's VAPID key (RFC 8292). The key and the subscriptions are kept in
// an encrypted file, like the other secrets.
package push

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var b64 = base64.RawURLEncoding

// Subscription is what the browser's PushManager.subscribe returns.
type Subscription struct {
	Endpoint string `json:"endpoint"`
	Keys     struct {
		P256DH string `json:"p256dh"`
		Auth   string `json:"auth"`
	} `json:"keys"`
}

// pushHosts are the push services browsers use. Subscriptions pointing
// anywhere else are refused, so a subscription can't make the agent send
// requests to arbitrary hosts.
var pushHosts = []string{
	"web.push.apple.com",                // Safari / iOS
	"fcm.googleapis.com",                // Chrome
	"updates.push.services.mozilla.com", // Firefox
	".notify.windows.com",               // Edge
}

// Validate checks a subscription before it's stored.
func (s Subscription) Validate() error {
	u, err := url.Parse(s.Endpoint)
	if err != nil || u.Scheme != "https" || u.User != nil {
		return errors.New("push: endpoint must be an https URL")
	}
	host := strings.ToLower(u.Hostname())
	ok := false
	for _, h := range pushHosts {
		if host == h || (strings.HasPrefix(h, ".") && strings.HasSuffix(host, h)) {
			ok = true
		}
	}
	if !ok {
		return fmt.Errorf("push: %s isn't a known push service", host)
	}
	if k, err := b64.DecodeString(strings.TrimRight(s.Keys.P256DH, "=")); err != nil || len(k) != 65 {
		return errors.New("push: bad p256dh key")
	}
	if a, err := b64.DecodeString(strings.TrimRight(s.Keys.Auth, "=")); err != nil || len(a) != 16 {
		return errors.New("push: bad auth secret")
	}
	return nil
}

// VAPID is the agent's signing key for push services.
type VAPID struct {
	key     *ecdsa.PrivateKey
	Subject string // mailto: or https: contact for push services
}

// NewVAPIDKey makes a new P-256 key and returns its private scalar.
func NewVAPIDKey() ([]byte, error) {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	return k.D.FillBytes(make([]byte, 32)), nil
}

// ParseVAPID loads a key from its private scalar.
func ParseVAPID(d []byte, subject string) (*VAPID, error) {
	if len(d) != 32 {
		return nil, errors.New("push: bad VAPID key")
	}
	k, err := ecdsa.ParseRawPrivateKey(elliptic.P256(), d)
	if err != nil {
		return nil, err
	}
	return &VAPID{key: k, Subject: subject}, nil
}

// PublicKey is the application server key the browser subscribes with.
func (v *VAPID) PublicKey() string {
	pub, _ := v.key.PublicKey.Bytes()
	return b64.EncodeToString(pub)
}

// jwt signs the token push services require (RFC 8292).
func (v *VAPID) jwt(aud string, now time.Time) (string, error) {
	head := b64.EncodeToString([]byte(`{"typ":"JWT","alg":"ES256"}`))
	claims, _ := json.Marshal(map[string]any{"aud": aud, "exp": now.Add(12 * time.Hour).Unix(), "sub": v.Subject})
	signing := head + "." + b64.EncodeToString(claims)
	h := sha256.Sum256([]byte(signing))
	r, s, err := ecdsa.Sign(rand.Reader, v.key, h[:])
	if err != nil {
		return "", err
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return signing + "." + b64.EncodeToString(sig), nil
}

// encrypt produces an aes128gcm body for one subscription (RFC 8291 §3-4).
// salt and asPriv are random in normal use; tests pass fixed ones.
func encrypt(plaintext []byte, sub Subscription, salt []byte, asPriv *ecdh.PrivateKey) ([]byte, error) {
	uaRaw, err := b64.DecodeString(strings.TrimRight(sub.Keys.P256DH, "="))
	if err != nil {
		return nil, err
	}
	auth, err := b64.DecodeString(strings.TrimRight(sub.Keys.Auth, "="))
	if err != nil {
		return nil, err
	}
	uaPub, err := ecdh.P256().NewPublicKey(uaRaw)
	if err != nil {
		return nil, err
	}
	shared, err := asPriv.ECDH(uaPub)
	if err != nil {
		return nil, err
	}
	asPub := asPriv.PublicKey().Bytes()

	// IKM = HKDF(salt=auth, ikm=ecdh, info="WebPush: info\0"||ua_pub||as_pub, 32)
	prkKey, err := hkdf.Extract(sha256.New, shared, auth)
	if err != nil {
		return nil, err
	}
	keyInfo := "WebPush: info\x00" + string(uaRaw) + string(asPub)
	ikm, err := hkdf.Expand(sha256.New, prkKey, keyInfo, 32)
	if err != nil {
		return nil, err
	}
	prk, err := hkdf.Extract(sha256.New, ikm, salt)
	if err != nil {
		return nil, err
	}
	cek, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: aes128gcm\x00", 16)
	if err != nil {
		return nil, err
	}
	nonce, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: nonce\x00", 12)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	record := append(append([]byte{}, plaintext...), 0x02) // last (and only) record
	const rs = 4096
	if len(record)+16 > rs {
		return nil, errors.New("push: message too long")
	}
	var head bytes.Buffer
	head.Write(salt)
	binary.Write(&head, binary.BigEndian, uint32(rs))
	head.WriteByte(byte(len(asPub)))
	head.Write(asPub)
	return gcm.Seal(head.Bytes(), nonce, record, nil), nil
}

// ErrGone means the push service no longer accepts this subscription (the
// app was removed or notifications turned off); forget it.
var ErrGone = errors.New("push: subscription expired")

// Send delivers one encrypted message.
func Send(ctx context.Context, client *http.Client, v *VAPID, sub Subscription, payload []byte, ttl time.Duration) error {
	if err := sub.Validate(); err != nil {
		return err
	}
	salt := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return err
	}
	asPriv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	body, err := encrypt(payload, sub, salt, asPriv)
	if err != nil {
		return err
	}
	u, _ := url.Parse(sub.Endpoint)
	token, err := v.jwt(u.Scheme+"://"+u.Host, time.Now())
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sub.Endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Encoding", "aes128gcm")
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("TTL", fmt.Sprint(int(ttl.Seconds())))
	req.Header.Set("Urgency", "high")
	req.Header.Set("Authorization", "vapid t="+token+", k="+v.PublicKey())
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	switch {
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
		return ErrGone
	case resp.StatusCode >= 300:
		return fmt.Errorf("push: %s", resp.Status)
	}
	return nil
}
