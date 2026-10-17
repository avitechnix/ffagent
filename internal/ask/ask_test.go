package ask

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/avitechnix/ffagent/internal/secrets"
)

func fakeAPI(t *testing.T, stop string, status int, seen *map[string]any, hdr *http.Header) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, seen)
		*hdr = r.Header.Clone()
		if status != 200 {
			w.WriteHeader(status)
			io.WriteString(w, `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"msg_1","type":"message","role":"assistant","model":"claude-opus-5-5",
		 "content":[{"type":"thinking","thinking":"","signature":"x"},{"type":"text","text":"Davis gets the bump because Hall is out."}],
		 "stop_reason":"`+stop+`","usage":{"input_tokens":10,"output_tokens":5}}`)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestAnswerRequestShape(t *testing.T) {
	var body map[string]any
	var hdr http.Header
	srv := fakeAPI(t, "end_turn", 200, &body, &hdr)
	c := New(secrets.New("sk-test"), "", option.WithBaseURL(srv.URL), option.WithMaxRetries(0))
	ans, err := c.Answer(context.Background(), "MOVE #1: Add Isaiah Davis", "why Davis?")
	if err != nil {
		t.Fatal(err)
	}
	if ans != "Davis gets the bump because Hall is out." {
		t.Errorf("answer = %q", ans)
	}
	if body["model"] != DefaultModel {
		t.Errorf("model = %v", body["model"])
	}
	if th, _ := body["thinking"].(map[string]any); th["type"] != "adaptive" {
		t.Errorf("thinking = %v", body["thinking"])
	}
	if oc, _ := body["output_config"].(map[string]any); oc["effort"] != "medium" {
		t.Errorf("output_config = %v", body["output_config"])
	}
	if body["fallbacks"] != "default" || !strings.Contains(hdr.Get("Anthropic-Beta"), "server-side-fallback-2026-07-01") {
		t.Errorf("fallbacks = %v, beta = %q", body["fallbacks"], hdr.Get("Anthropic-Beta"))
	}
	msgs, _ := body["messages"].([]any)
	if len(msgs) != 1 || !strings.Contains(mustJSON(msgs[0]), "Manager's question: why Davis?") || !strings.Contains(mustJSON(msgs[0]), "<agent_data>") {
		t.Errorf("messages = %v", msgs)
	}
	if !strings.Contains(mustJSON(body["system"]), "can't approve") {
		t.Error("system prompt missing")
	}
	if hdr.Get("X-Api-Key") != "sk-test" {
		t.Error("API key not sent")
	}
}

func TestRefusalAndAuthErrors(t *testing.T) {
	var body map[string]any
	var hdr http.Header
	srv := fakeAPI(t, "refusal", 200, &body, &hdr)
	c := New(secrets.New("k"), "", option.WithBaseURL(srv.URL), option.WithMaxRetries(0))
	if _, err := c.Answer(context.Background(), "d", "q"); err == nil || !strings.Contains(err.Error(), "declined") {
		t.Errorf("refusal: %v", err)
	}
	srv2 := fakeAPI(t, "", 401, &body, &hdr)
	c2 := New(secrets.New("bad"), "", option.WithBaseURL(srv2.URL), option.WithMaxRetries(0))
	_, err := c2.Answer(context.Background(), "d", "q")
	if err == nil || !strings.Contains(err.Error(), "secrets set anthropic") || strings.Contains(err.Error(), "bad") {
		t.Errorf("auth error: %v", err)
	}
}

func mustJSON(v any) string { return fmt.Sprint(v) }
