package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/submission"
	"github.com/Autumn-27/norma/llm"
)

func submissionFixture(t *testing.T) (*Server, submission.Draft) {
	t.Helper()
	if os.Getenv("ARTEX_PG_DSN") == "" {
		t.Skip("requires isolated PostgreSQL")
	}
	dsn, _, err := db.DSN()
	if err != nil {
		t.Fatal(err)
	}
	pg, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	fid, err := pg.AddFinding(0, 0, "IDOR", "Disclosure test", "high", "verified summary", "Authorization: Bearer do-not-send-this-secret\nProof: test invoice", "worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = pg.Exec(`UPDATE findings SET status='confirmed' WHERE id=$1`, fid)
	if err != nil {
		t.Fatal(err)
	}
	p, err := pg.SaveSubmissionProfile(t.Context(), submission.Profile{Name: "Test program", Channel: "hackerone", Destination: "https://hackerone.com/test", Rules: "State impact and test account roles", Language: "en", Fields: []submission.Field{{ID: "roles", Label: "Account roles", Required: true}}})
	if err != nil {
		t.Fatal(err)
	}
	draft, err := pg.PrepareSubmission(t.Context(), strconv.FormatInt(fid, 10), p.ID)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{m: &Manager{pg: pg}, ctx: context.Background(), llmCfg: agent.Config{Model: "fake-model"}, jwtKey: []byte("submission-test-key")}
	t.Cleanup(func() {
		_, _ = pg.DeleteFinding(fid)
		_, _ = pg.Exec(`DELETE FROM submission_profiles WHERE id=$1`, p.ID)
		pg.Close()
	})
	return s, draft
}
func callDisclosure(t *testing.T, handler func(http.ResponseWriter, *http.Request), id string, body any, want int) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	r := httptest.NewRequest("POST", "/", strings.NewReader(string(b)))
	r.SetPathValue("id", id)
	w := httptest.NewRecorder()
	handler(w, r)
	if w.Code != want {
		t.Fatalf("status %d want %d: %s", w.Code, want, w.Body.String())
	}
	return w
}

func TestSubmissionDraftLifecycle(t *testing.T) {
	s, d := submissionFixture(t)
	again, err := s.m.pg.PrepareSubmission(t.Context(), d.FindingID, d.ProfileID)
	if err != nil || again.ID != d.ID {
		t.Fatal("prepare is not idempotent", err)
	}
	callDisclosure(t, s.confirmSubmission, d.ID, map[string]any{"revision": 1, "reviewed": true}, 409)
	w := callDisclosure(t, s.editSubmission, d.ID, map[string]any{"revision": 1, "title": "Title", "body": "Steps", "impact": "Confirmed impact", "values": map[string]string{"roles": "Two test members"}}, 200)
	var v submission.View
	_ = json.Unmarshal(w.Body.Bytes(), &v)
	callDisclosure(t, s.editSubmission, d.ID, map[string]any{"revision": 1}, 409)
	w = callDisclosure(t, s.confirmSubmission, d.ID, map[string]any{"revision": v.Draft.Revision, "reviewed": true}, 200)
	_ = json.Unmarshal(w.Body.Bytes(), &v)
	r := httptest.NewRequest("GET", "/?revision="+strconv.FormatInt(v.Draft.Revision, 10), nil)
	r.SetPathValue("id", d.ID)
	out := httptest.NewRecorder()
	s.exportSubmission(out, r)
	if out.Code != 200 || !strings.Contains(out.Body.String(), "Two test members") {
		t.Fatal("export failed", out.Code, out.Body.String())
	}
	_, _ = s.m.pg.Exec(`UPDATE findings SET evidence='changed evidence',evidence_version=evidence_version+1 WHERE id=$1`, d.FindingID)
	out = httptest.NewRecorder()
	s.exportSubmission(out, r)
	if out.Code != 409 {
		t.Fatal("stale export permitted")
	}
	callDisclosure(t, s.resetSubmission, d.ID, map[string]any{"revision": v.Draft.Revision}, 200)
}

func TestSubmissionModelIsolationAndConflicts(t *testing.T) {
	s, d := submissionFixture(t)
	s.llmDirect = retestProvider{complete: func(ctx context.Context, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
		if len(req.Tools) != 0 || req.MaxTokens > 4096 {
			t.Fatal("unexpected tool or output budget")
		}
		if strings.Contains(req.Messages[0].Text(), "do-not-send-this-secret") {
			t.Fatal("unredacted credential sent to model")
		}
		return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock(`{"title":"Draft","body":"Repro steps","impact":"Verified impact","fields":[{"id":"roles","label":"roles","required":true,"value":"","sources":[]}],"warnings":[]}`)}}, "end_turn", llm.Usage{}, nil
	}}
	callDisclosure(t, s.generateSubmission, d.ID, map[string]any{"revision": 1, "allow_model": false}, 400)
	w := callDisclosure(t, s.generateSubmission, d.ID, map[string]any{"revision": 1, "allow_model": true}, 200)
	var v submission.View
	_ = json.Unmarshal(w.Body.Bytes(), &v)
	if v.Draft.Model != "fake-model" || v.Draft.Fields[0].Label != "Account roles" {
		t.Fatalf("unexpected model draft: %+v", v.Draft)
	}
	s.llmDirect = retestProvider{complete: func(ctx context.Context, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
		_, err := s.m.pg.Exec(`UPDATE findings SET summary='changed while generating' WHERE id=$1`, d.FindingID)
		if err != nil {
			t.Fatal(err)
		}
		return llm.UserText(`{"title":"Draft","body":"Steps","fields":[],"warnings":[]}`), "end_turn", llm.Usage{}, nil
	}}
	callDisclosure(t, s.generateSubmission, d.ID, map[string]any{"revision": v.Draft.Revision, "allow_model": true}, 409)
	current, err := s.m.pg.SubmissionView(t.Context(), d.ID)
	if err != nil || current.Draft.Revision != v.Draft.Revision {
		t.Fatal("conflicting generation overwrote original draft", err)
	}
}
