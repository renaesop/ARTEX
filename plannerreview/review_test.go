package plannerreview

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func answer(model string) map[string]any {
	d := Decision{Status: "needs_evidence", Reason: "Missing recorded checkpoint", Requests: []EvidenceRequest{{1, "checkpoint"}}}
	b, _ := json.Marshal(d)
	return map[string]any{"model": model, "choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]any{"content": string(b)}}}, "usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 20, "cost": 0.1}}
}
func TestReviewFallbackOrderAndSupplement(t *testing.T) {
	var got []string
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		model := body["model"].(string)
		got = append(got, model)
		if _, ok := body["tools"]; ok {
			t.Error("review unexpectedly has tools")
		}
		if body["reasoning"].(map[string]any)["effort"] != "high" {
			t.Error("wrong effort")
		}
		if body["provider"].(map[string]any)["allow_fallbacks"] != false {
			t.Error("implicit provider fallback enabled")
		}
		if len(got) <= 3 {
			w.WriteHeader([]int{502, 429, 503}[len(got)-1])
			w.Write([]byte(`{"error":{"code":503}}`))
			return
		}
		json.NewEncoder(w).Encode(answer(model))
	}))
	defer s.Close()
	c := Client{Endpoint: s.URL, APIKey: "test"}
	r := c.Review(t.Context(), Input{}, "", 1, nil)
	if !reflect.DeepEqual(got, Models()) || r.Decision == nil || r.Status != "completed" {
		t.Fatalf("order/result: %v %+v", got, r)
	}
	c.Review(t.Context(), Input{}, r.Model, 2, nil)
	if len(got) != 5 || got[4] != Models()[3] {
		t.Fatalf("supplement changed model: %v", got)
	}
}
func TestReviewTerminalResponsesDoNotFallBack(t *testing.T) {
	cases := []struct {
		name, body, status string
		code               int
	}{
		{"refusal", `{"choices":[{"finish_reason":"content_filter","message":{"refusal":"policy"}}]}`, "refused", 200},
		{"policy inside 200", `{"error":{"code":403,"metadata":{"error_type":"content_policy_violation","provider_code":"SAFETY"}}}`, "policy_block", 200},
		{"auth", `{"error":{"code":401}}`, "provider_error", 401},
		{"credits", `{"error":{"code":402}}`, "provider_error", 402},
		{"invalid response", `{"choices":[]}`, "invalid_response", 200},
		{"mismatched model", `{"model":"other","choices":[{"finish_reason":"stop","message":{"content":"{}"}}]}`, "model_mismatch", 200},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.WriteHeader(tc.code)
				w.Write([]byte(tc.body))
			}))
			defer s.Close()
			c := Client{Endpoint: s.URL}
			out := c.Review(t.Context(), Input{}, "", 1, nil)
			if calls != 1 || out.Status != tc.status {
				t.Fatalf("calls=%d result=%+v", calls, out)
			}
		})
	}
}
func TestReviewNeedsEvidenceStopsAndAuditFailureStops(t *testing.T) {
	calls := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; json.NewEncoder(w).Encode(answer(Models()[0])) }))
	defer s.Close()
	c := Client{Endpoint: s.URL}
	out := c.Review(t.Context(), Input{}, "", 1, nil)
	if calls != 1 || out.Decision == nil || out.Decision.Status != "needs_evidence" {
		t.Fatal(out, calls)
	}
	out = c.Review(t.Context(), Input{}, "", 1, func(Attempt) error { return errors.New("storage down") })
	if calls != 2 || out.Status != "audit_failed" {
		t.Fatal(out, calls)
	}
}
func TestReviewCancellationAndRedirect(t *testing.T) {
	started := make(chan struct{})
	ctx, cancel := context.WithCancel(t.Context())
	release := make(chan struct{})
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(started); <-release }))
	defer func() { close(release); s.Close() }()
	c := Client{Endpoint: s.URL, AttemptTimeout: time.Second}
	done := make(chan Result, 1)
	go func() { done <- c.Review(ctx, Input{}, "", 1, nil) }()
	<-started
	cancel()
	out := <-done
	if out.Status != "canceled" || len(out.Attempts) != 1 {
		t.Fatal(out)
	}
	leaked := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked = true }))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 307) }))
	defer redirect.Close()
	c.Endpoint = redirect.URL
	out = c.Review(t.Context(), Input{}, "", 1, nil)
	if leaked || len(out.Attempts) != 1 || out.Status != "invalid_response" {
		t.Fatal("redirect followed", out)
	}
}
func TestReviewEvidenceValidationAndSanitization(t *testing.T) {
	for _, r := range []EvidenceRequest{{1, "Bash"}, {0, "summary"}, {1, "password"}} {
		if r.Valid() {
			t.Fatalf("accepted %+v", r)
		}
	}
	if (Decision{Status: "needs_evidence", Reason: "more context"}).Validate() == nil {
		t.Fatal("vague evidence request accepted")
	}
	in := Input{Round: Round{FinalText: "password: secret-value\nauthorization: Bearer test-secret"}, Snapshot: Snapshot{Nodes: []Node{{ID: 9007199254740993, Summary: "sk-abcdefghijklmnopqrstu"}}}}
	b, err := sanitizedInput(in)
	if err != nil || strings.Contains(string(b), "secret-value") || strings.Contains(string(b), "abcdefghijklmnopqrstu") || !strings.Contains(string(b), "9007199254740993") {
		t.Fatalf("sanitization/ID preservation: %s %v", b, err)
	}
}
