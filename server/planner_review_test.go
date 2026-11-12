package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/plannerreview"
)

func reviewFixture(t *testing.T) (*Engine, *Task, int64) {
	t.Helper()
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	created, err := m.CreateTaskWithOptions("online review test", "audit historical evidence", db.TaskCreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	task, ok := m.Task(created.ID)
	if !ok {
		t.Fatal("task missing")
	}
	tid, _ := strconv.ParseInt(task.ID, 10, 64)
	t.Cleanup(func() { m.pg.DeleteTask(tid) })
	goal, err := task.Store.AddGoal(map[string]any{"text": "audit existing records"}, "test")
	if err != nil {
		t.Fatal(err)
	}
	pid, err := m.pg.SaveProfile(&db.LLMProfile{Name: fmt.Sprintf("review-test-%d", time.Now().UnixNano()), Format: "openai", Model: "unused", BaseURL: "https://openrouter.ai/api/v1", APIKey: "test-key"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.pg.DeleteProfile(pid) })
	old, exists, err := m.pg.GetSetting("planner_review")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if exists {
			m.pg.SetSetting("planner_review", old)
		} else {
			m.pg.Exec(`DELETE FROM settings WHERE key='planner_review'`)
		}
	})
	if err = m.pg.SetSetting("planner_review", fmt.Sprintf(`{"enabled":true,"profile_id":%d}`, pid)); err != nil {
		t.Fatal(err)
	}
	return NewEngine(m), task, goal
}
func emitReviewAnswer(w http.ResponseWriter, model string, d plannerreview.Decision) {
	b, _ := json.Marshal(d)
	json.NewEncoder(w).Encode(map[string]any{"model": model, "choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]any{"content": string(b)}}}, "usage": map[string]any{"prompt_tokens": 2, "completion_tokens": 3}})
}
func TestOnlinePlannerReviewBoundedSupplementAndRestart(t *testing.T) {
	e, task, goal := reviewFixture(t)
	calls := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var b struct {
			Model    string `json:"model"`
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&b)
		var in plannerreview.Input
		json.Unmarshal([]byte(b.Messages[1].Content), &in)
		if calls == 1 && len(in.Evidence) != 0 {
			t.Error("unexpected supplement")
		}
		if calls == 2 && (len(in.Evidence) != 1 || in.Evidence[0].Value != "audit existing records") {
			t.Error("missing scoped evidence", in.Evidence)
		}
		emitReviewAnswer(w, b.Model, plannerreview.Decision{Status: "needs_evidence", Reason: "Still missing coverage", Requests: []plannerreview.EvidenceRequest{{NodeID: goal, Field: "summary"}}})
	}))
	defer s.Close()
	e.reviewClientForTest = func(key string) *plannerreview.Client { return &plannerreview.Client{APIKey: key, Endpoint: s.URL} }
	round := plannerreview.Round{TerminalReason: "completed", StartedAt: time.Now(), EndedAt: time.Now()}
	e.finishPlannerRound(t.Context(), task, round, nil)
	if calls != 2 {
		t.Fatalf("expected exactly one supplement, got %d calls", calls)
	}
	var state string
	var attempts int
	if err := e.m.pg.QueryRow(`SELECT state,jsonb_array_length(attempts) FROM planner_review_runs WHERE task_id=$1`, task.ID).Scan(&state, &attempts); err != nil || state != "waiting_evidence" || attempts != 2 {
		t.Fatal(state, attempts, err)
	}
	e.finishPlannerRound(t.Context(), task, round, nil)
	if calls != 2 {
		t.Fatal("same evidence reviewed again")
	}
	if !NewEngine(e.m).skipReviewedIdle(t.Context(), task) {
		t.Fatal("restart lost persistent suppression")
	}
	if _, err := task.Store.AddNode(db.KindHint, map[string]any{"text": "new scheduling evidence"}, 1, "active", "test", nil); err != nil {
		t.Fatal(err)
	}
	if e.skipReviewedIdle(t.Context(), task) {
		t.Fatal("new evidence did not wake review")
	}
}
func TestOnlinePlannerReviewStaleAndCancel(t *testing.T) {
	for _, mode := range []string{"stale", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			e, task, goal := reviewFixture(t)
			calls := 0
			ctx := e.execContextFor(t.Context(), task.ID)
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if mode == "stale" {
					_, err := task.Store.AddIntent(map[string]any{"summary": "operator created work"}, 1, nil, "human")
					if err != nil {
						t.Error(err)
					}
				} else {
					e.Pause(task.ID, context.Canceled)
				}
				emitReviewAnswer(w, plannerreview.Models()[0], plannerreview.Decision{Status: "supported_zero", Reason: "wait", EvidenceIDs: []int64{goal}, ResumeCondition: "record changes"})
			}))
			defer s.Close()
			e.reviewClientForTest = func(key string) *plannerreview.Client { return &plannerreview.Client{APIKey: key, Endpoint: s.URL} }
			e.finishPlannerRound(ctx, task, plannerreview.Round{TerminalReason: "completed"}, nil)
			var state string
			if err := e.m.pg.QueryRow(`SELECT state FROM planner_review_runs WHERE task_id=$1`, task.ID).Scan(&state); err != nil || state != mode || calls != 1 {
				t.Fatal(state, calls, err)
			}
		})
	}
}

func TestOnlinePlannerReviewEligibility(t *testing.T) {
	e, task, _ := reviewFixture(t)
	calls := 0
	e.reviewClientForTest = func(string) *plannerreview.Client { calls++; return &plannerreview.Client{} }
	for _, r := range []plannerreview.Round{
		{TerminalReason: "completed", CreatedIntentIDs: []int64{100}},
		{TerminalReason: "completed", GoalMet: true},
		{TerminalReason: "timeout"},
	} {
		e.finishPlannerRound(t.Context(), task, r, nil)
	}
	e.finishPlannerRound(t.Context(), task, plannerreview.Round{TerminalReason: "completed"}, errors.New("model unavailable"))
	if _, err := task.Store.AddIntent(map[string]any{"summary": "existing queued work"}, 1, nil, "test"); err != nil {
		t.Fatal(err)
	}
	e.finishPlannerRound(t.Context(), task, plannerreview.Round{TerminalReason: "completed"}, nil)
	if calls != 0 {
		t.Fatal("review called for ineligible round", calls)
	}
}
