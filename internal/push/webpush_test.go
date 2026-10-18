package push

import (
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/sha256"
	"encoding/json"
	"math/big"
	"strings"
	"testing"
	"time"
)

// RFC 8291 Appendix A: encrypting "When I grow up, I want to be a
// watermelon" with fixed keys and salt must give exactly this body.
func TestEncryptRFC8291Vector(t *testing.T) {
	dec := func(s string) []byte {
		b, err := b64.DecodeString(s)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	asPriv, err := ecdh.P256().NewPrivateKey(dec("yfWPiYE-n46HLnH0KqZOF1fJJU3MYrct3AELtAQ-oRw"))
	if err != nil {
		t.Fatal(err)
	}
	if got := b64.EncodeToString(asPriv.PublicKey().Bytes()); got != "BP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27mlmlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A8" {
		t.Fatalf("application server public key %s", got)
	}
	uaPriv, err := ecdh.P256().NewPrivateKey(dec("q1dXpw3UpT5VOmu_cf_v6ih07Aems3njxI-JWgLcM94"))
	if err != nil {
		t.Fatal(err)
	}
	var sub Subscription
	sub.Endpoint = "https://web.push.apple.com/x"
	sub.Keys.P256DH = b64.EncodeToString(uaPriv.PublicKey().Bytes())
	sub.Keys.Auth = "BTBZMqHH6r4Tts7J_aSIgg"
	if sub.Keys.P256DH != "BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4" {
		t.Fatalf("user agent public key %s", sub.Keys.P256DH)
	}
	body, err := encrypt([]byte("When I grow up, I want to be a watermelon"), sub, dec("DGv6ra1nlYgDCS1FRnbzlw"), asPriv)
	if err != nil {
		t.Fatal(err)
	}
	want := "DGv6ra1nlYgDCS1FRnbzlwAAEABBBP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27mlmlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A_yl95bQpu6cVPTpK4Mqgkf1CXztLVBSt2Ks3oZwbuwXPXLWyouBWLVWGNWQexSgSxsj_Qulcy4a-fN"
	if got := b64.EncodeToString(body); got != want {
		t.Errorf("body\n got %s\nwant %s", got, want)
	}
}

func TestVAPIDToken(t *testing.T) {
	d, err := NewVAPIDKey()
	if err != nil {
		t.Fatal(err)
	}
	v, err := ParseVAPID(d, "mailto:bot@example.com")
	if err != nil {
		t.Fatal(err)
	}
	tok, err := v.jwt("https://web.push.apple.com", time.Unix(1700000000, 0))
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatal(tok)
	}
	var claims map[string]any
	c, _ := b64.DecodeString(parts[1])
	json.Unmarshal(c, &claims)
	if claims["aud"] != "https://web.push.apple.com" || claims["sub"] != "mailto:bot@example.com" || claims["exp"].(float64) != 1700000000+12*3600 {
		t.Errorf("claims %v", claims)
	}
	sig, _ := b64.DecodeString(parts[2])
	h := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if len(sig) != 64 || !ecdsa.Verify(&v.key.PublicKey, h[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
		t.Error("signature doesn't verify")
	}
	if pk, _ := b64.DecodeString(v.PublicKey()); len(pk) != 65 || pk[0] != 4 {
		t.Errorf("public key %x", pk)
	}
}

func TestSubscriptionValidate(t *testing.T) {
	good := `{"endpoint":"https://web.push.apple.com/QH7f...","keys":{"p256dh":"BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4","auth":"BTBZMqHH6r4Tts7J_aSIgg"}}`
	var s Subscription
	json.Unmarshal([]byte(good), &s)
	if err := s.Validate(); err != nil {
		t.Errorf("good subscription: %v", err)
	}
	for _, ep := range []string{"http://web.push.apple.com/x", "https://evil.example.com/x", "https://127.0.0.1/x", "https://user@web.push.apple.com/x", "https://web.push.apple.com.evil.com/x"} {
		bad := s
		bad.Endpoint = ep
		if bad.Validate() == nil {
			t.Errorf("accepted %s", ep)
		}
	}
	if s2 := (Subscription{Endpoint: "https://wns2-par02p.notify.windows.com/w/x"}); s2.Validate() == nil {
		t.Error("accepted missing keys")
	}
}
