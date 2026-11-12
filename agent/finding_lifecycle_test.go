package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
)

func TestFindingLifecycleAcrossInheritedTools(t *testing.T) {
	d := testDB(t)
	defer d.Close()
	source, err := d.CreateTask("lifecycle source", "review", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(source.ID)
	current, err := d.CreateTaskWithOptions("lifecycle reader", "review", db.TaskCreateOptions{SourceTaskIDs: []int64{source.ID}})
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(current.ID)
	ts := d.Exploration(source.ExplorationID)
	nid, err := ts.AddNode(db.KindFinding, map[string]any{"summary": "old success"}, 1, "confirmed", "worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	fid, err := d.AddFinding(source.ID, nid, "test", "old success", "low", "old success", "historical evidence", "worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	tools := NewToolSet(nil, "")
	tools.ts, tools.taskID = d.Exploration(current.ExplorationID), current.ID
	for _, status := range []string{db.FindingConfirmed, db.FindingFixed} {
		if _, err := d.Exec(`UPDATE findings SET status=$1 WHERE id=$2`, status, fid); err != nil {
			t.Fatal(err)
		}
		list, err := tools.listFindings().Call(t.Context(), json.RawMessage(`{}`), nil)
		if err != nil {
			t.Fatal(err)
		}
		detail, err := tools.nodeDetail().Call(t.Context(), json.RawMessage(fmt.Sprintf(`{"id":%d}`, nid)), nil)
		if err != nil {
			t.Fatal(err)
		}
		overview, _ := json.Marshal(tools.graphOverviewData())
		for name, value := range map[string]string{"list": list.Flatten(), "detail": detail.Flatten(), "overview": string(overview)} {
			var normalized any
			if err := json.Unmarshal([]byte(value), &normalized); err != nil {
				t.Fatalf("%s: %s", name, value)
			}
			raw, _ := json.Marshal(normalized)
			if !strings.Contains(string(raw), `"finding_status":"`+status+`"`) {
				t.Fatalf("%s lost current inherited status: %s", name, raw)
			}
		}
	}
}
