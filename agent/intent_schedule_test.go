package agent

import (
	"encoding/json"
	"testing"
	"time"
)

func TestScheduledIntentBatchAndOverview(t *testing.T) {
	d := testDB(t)
	defer d.Close()
	task, err := d.CreateTask("scheduled observations", "test", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(task.ID)
	ts := d.Exploration(task.ExplorationID)
	tools := NewToolSet(ts, "planner")
	tools.SetTaskID(task.ID)
	due := time.Now().Add(time.Hour).Format(time.RFC3339)
	in, _ := json.Marshal(map[string]any{"intents": []any{
		map[string]any{"summary": "delayed observation", "not_before": due},
		map[string]any{"summary": "independent work"},
	}})
	tool := DecorateTool(tools.addIntent(), "custom description", obj(map[string]any{}))
	props := tool.InputSchema()["properties"].(map[string]any)
	if props["intents"].(map[string]any)["items"].(map[string]any)["properties"].(map[string]any)["not_before"] == nil {
		t.Fatal("persisted batch schema lost scheduling")
	}
	r, err := tool.Call(t.Context(), in, nil)
	if err != nil || r.IsError {
		t.Fatal(r, err)
	}
	var ids struct {
		IDs []int64 `json:"ids"`
	}
	if err := json.Unmarshal([]byte(r.Flatten()), &ids); err != nil || len(ids.IDs) != 2 {
		t.Fatal(r, err)
	}
	if ok, err := ts.ClaimIntent(ids.IDs[0], "worker"); err != nil || ok {
		t.Fatal("early execution", err)
	}
	if ok, err := ts.ClaimIntent(ids.IDs[1], "worker"); err != nil || !ok {
		t.Fatal("ready work blocked", err)
	}
	fr, err := ts.Frontier(20)
	if err != nil {
		t.Fatal(err)
	}
	compact := compactIntents(fr, nil, nil)
	if len(compact) != 1 || compact[0]["not_before"] == nil {
		t.Fatal("planner cannot see deferred work", compact)
	}
	r, err = tool.Call(t.Context(), json.RawMessage(`{"summary":"invalid schedule","not_before":"tomorrow"}`), nil)
	if err != nil || !r.IsError {
		t.Fatal("invalid schedule admitted", r, err)
	}
	if n, _ := ts.CountOpenIntents(); n != 1 {
		t.Fatal("bad schedule created an orphan", n)
	}
}
