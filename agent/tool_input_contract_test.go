package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
	actool "github.com/Autumn-27/norma/tool"
)

func TestToolContractRepairsPersistedSchema(t *testing.T) {
	old := map[string]any{"type": "object", "properties": map[string]any{}, "required": []any{""}}
	tool := DecorateTool(NewToolSet(nil, "").listTaskCredentials(), "edited", old)
	if err := actool.ValidateInput(tool.InputSchema(), json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	if tool.Description() != "edited" || len(old["required"].([]any)) != 1 {
		t.Fatal("description or shared schema mutated")
	}
	legacy := obj(map[string]any{
		"summary":      map[string]any{"type": "string", "description": "edited summary"},
		"confidence":   map[string]any{"type": "string", "default": "inferred"},
		"facts":        map[string]any{"type": "array", "items": obj(nil)},
		"custom_field": str("keep"),
	})
	fixed := RepairBuiltinToolSchema("record_fact", legacy)
	props := fixed["properties"].(map[string]any)
	if props["summary"].(map[string]any)["description"] != "edited summary" || props["confidence"].(map[string]any)["default"] != "inferred" || props["custom_field"] == nil {
		t.Fatal("custom annotations lost")
	}
	item := props["facts"].(map[string]any)["items"].(map[string]any)
	if item["properties"].(map[string]any)["summary"] == nil || item["properties"].(map[string]any)["supersedes"] == nil {
		t.Fatal("batch schema incomplete")
	}
	if len(fixed["anyOf"].([]any)) != 2 || item["required"].([]string)[0] != "summary" {
		t.Fatal("single/batch required fields missing")
	}
	first, _ := json.Marshal(fixed)
	second, _ := json.Marshal(RepairBuiltinToolSchema("record_fact", fixed))
	if string(first) != string(second) {
		t.Fatal("repair is not idempotent")
	}
}

func TestToolContractCredentialEmptyInput(t *testing.T) {
	d := testDB(t)
	defer d.Close()
	task, err := d.CreateTask("credential contract", "test", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(task.ID)
	tools := NewToolSet(d.Exploration(task.ExplorationID), "worker")
	tools.taskID = task.ID
	tool := tools.listTaskCredentials()
	// Simulate the persisted schema's JSON round trip and the SDK validation.
	raw, _ := json.Marshal(tool.InputSchema())
	var schema map[string]any
	_ = json.Unmarshal(raw, &schema)
	if err := actool.ValidateInput(schema, json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	result, err := tool.Call(t.Context(), json.RawMessage(`{}`), nil)
	if err != nil || result.IsError || !strings.Contains(result.Flatten(), "credentials") {
		t.Fatalf("empty credential query: %+v %v", result, err)
	}
}

func TestToolContractFactCallsAndPartialFailure(t *testing.T) {
	d := testDB(t)
	defer d.Close()
	task, err := d.CreateTask("fact contract", "test", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(task.ID)
	ts := d.Exploration(task.ExplorationID)
	intent, err := ts.AddNode(db.KindIntent, map[string]any{"summary": "check"}, 1, "running", "planner", nil)
	if err != nil {
		t.Fatal(err)
	}
	tools := NewToolSet(ts, "worker")
	tools.ownerNode = intent
	tool := tools.recordFact()
	for _, input := range []string{
		`{}`, `null`, `{"supersedes":1,"test_dimension":"check"}`,
		`{"summary":" "}`, `{"summary":"must not fall back","facts":[]}`,
		`{"summary":"must not fall back","facts":null}`,
		`{"facts":[{"summary":"must not partially save"},{"summary":42}]}`,
	} {
		result, err := tool.Call(t.Context(), json.RawMessage(input), nil)
		if err != nil || !result.IsError {
			t.Fatalf("accepted %s: %+v %v", input, result, err)
		}
	}
	if tools.Writes().Facts != 0 {
		t.Fatal("invalid input wrote a fact")
	}
	result, err := tool.Call(t.Context(), json.RawMessage(`{"facts":[{"summary":"first valid fact"},{"summary":" ","detail":"invalid"}]}`), nil)
	if err != nil || !result.IsError {
		t.Fatalf("partial failure not flagged: %+v %v", result, err)
	}
	var batch struct {
		IDs    []int64           `json:"ids"`
		Errors map[string]string `json:"errors"`
		Retry  string            `json:"retry_instruction"`
	}
	if err := json.Unmarshal([]byte(result.Flatten()), &batch); err != nil {
		t.Fatal(err)
	}
	if len(batch.IDs) != 2 || batch.IDs[0] <= 0 || batch.IDs[1] != 0 || batch.Errors["1"] == "" || batch.Retry == "" || tools.Writes().Facts != 1 {
		t.Fatalf("partial result: %+v", batch)
	}
	result, err = tool.Call(t.Context(), json.RawMessage(`{"facts":[{"summary":"first valid fact"}]}`), nil)
	if err != nil || result.IsError || tools.Writes().Facts != 1 {
		t.Fatal("retry duplicated fact", err)
	}
	result, err = tool.Call(t.Context(), json.RawMessage(`{"summary":"single mode remains supported"}`), nil)
	if err != nil || result.IsError || tools.Writes().Facts != 2 {
		t.Fatal("single mode failed", err)
	}
}
