package agent

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/Autumn-27/artex/plannerreview"
	"github.com/Autumn-27/norma/llm"
)

func TestPlannerRoundCountsSuccessfulIntentsOnly(t *testing.T) {
	d := testDB(t)
	defer d.Close()
	task, err := d.CreateTask("planner result", "local fixture", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(task.ID)
	tsx := NewToolSet(d.Exploration(task.ExplorationID), "planner")
	_, err = tsx.addOneIntent(intentItem{Summary: ""})
	if err == nil {
		t.Fatal("empty intent accepted")
	}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := tsx.addOneIntent(intentItem{Summary: "test bookkeeping"})
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	ids, decision := tsx.plannerOutcome()
	if len(ids) != 8 || decision != nil {
		t.Fatal(ids, decision)
	}
	for _, id := range ids {
		if err := tsx.ts.SetIntentState(id, "done"); err != nil {
			t.Fatal(err)
		}
	}
	// Fast completion leaves an empty queue but MUST NOT turn this into zero production.
	ids, _ = tsx.plannerOutcome()
	if len(ids) != 8 {
		t.Fatal("lost per-round creations")
	}
}

func TestPlannerRoundPreservesSDKDecisionAndTerminal(t *testing.T) {
	d := testDB(t)
	defer d.Close()
	task, err := d.CreateTask("planner sdk", "review records", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(task.ID)
	calls := 0
	provider := captureUsageProvider{stream: func(_ context.Context, yield func(llm.StreamEvent, error) bool) {
		calls++
		var events []llm.StreamEvent
		if calls == 1 {
			events = []llm.StreamEvent{
				{Type: llm.SEToolUseStart, ToolID: "decision", ToolName: "finish_planning"},
				{Type: llm.SEToolInputJSON, Text: `{"reason":"insufficient_evidence","evidence_ids":[],"explanation":"no recorded reason","resume_condition":"missing record supplied"}`},
				{Type: llm.SEMessageDelta, StopReason: "tool_use"},
			}
		} else {
			events = []llm.StreamEvent{{Type: llm.SETextDelta, Text: "Review needed; no new intents."}, {Type: llm.SEMessageDelta, StopReason: "end_turn"}}
		}
		for _, event := range events {
			if !yield(event, nil) {
				return
			}
		}
	}}
	p := NewPlanner(provider, "mock", t.TempDir(), nil, 0, 4)
	r, err := p.PlanRound(t.Context(), task.ID, nil, d.Exploration(task.ExplorationID), "review scheduling", nil, nil)
	if err != nil || r.Zero == nil || r.TerminalReason != "completed" || r.FinalText != "Review needed; no new intents." || len(r.CreatedIntentIDs) != 0 || calls != 2 {
		t.Fatalf("result=%+v calls=%d err=%v", r, calls, err)
	}
}
func TestPlannerZeroDecisionValidation(t *testing.T) {
	d := testDB(t)
	defer d.Close()
	task, err := d.CreateTask("planner zero", "fixture", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(task.ID)
	tsx := NewToolSet(d.Exploration(task.ExplorationID), "planner")
	goal, err := tsx.ts.AddGoal(map[string]any{"text": "verify scheduling"}, "test")
	if err != nil {
		t.Fatal(err)
	}
	decision := plannerreview.ZeroDecision{Reason: "waiting_dependency", EvidenceIDs: []int64{goal}, Explanation: "Waiting for recorded external dependency", ResumeCondition: "dependency record changes"}
	in, _ := json.Marshal(decision)
	tool := tsx.finishPlanning()
	// CoreTool invocation uses the same runtime path as other tool tests.
	res, err := tool.Call(t.Context(), in, nil)
	if err != nil || res.IsError {
		t.Fatal(res, err)
	}
	_, got := tsx.plannerOutcome()
	if got == nil || got.Reason != "waiting_dependency" {
		t.Fatal(got)
	}
	if !settlementWrite("finish_planning") {
		t.Fatal("settlement cannot persist decision")
	}
}
