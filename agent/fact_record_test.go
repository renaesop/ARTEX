package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"github.com/Autumn-27/artex/db"
	actool "github.com/Autumn-27/norma/tool"
)

func TestRecordFactConcurrentRetryRevisionAndIsolation(t *testing.T) {
	d := testDB(t)
	defer d.Close()
	task, err := d.CreateTask("fact idempotency", "test", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(task.ID)
	ts := d.Exploration(task.ExplorationID)
	intent, err := ts.AddNode(db.KindIntent, map[string]any{"summary": "check"}, 1, "running", "planner", nil)
	if err != nil {
		t.Fatal(err)
	}
	const count = 16
	ids := make([]int64, count)
	created := make([]bool, count)
	errs := make([]error, count)
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ids[i], created[i], errs[i] = ts.RecordFact(t.Context(), intent, map[string]any{"summary": "same", "detail": "observed"}, nil, "worker", 0)
		}(i)
	}
	wg.Wait()
	writes := 0
	for i := 0; i < count; i++ {
		if errs[i] != nil || ids[i] != ids[0] || ids[i] == 0 {
			t.Fatalf("retry %d: id=%d err=%v", i, ids[i], errs[i])
		}
		if created[i] {
			writes++
		}
	}
	if writes != 1 {
		t.Fatalf("created %d facts", writes)
	}
	replacement := map[string]any{"summary": "revised", "supersedes": ids[0]}
	id, new, err := ts.RecordFact(t.Context(), intent, replacement, nil, "worker", ids[0])
	if err != nil || !new {
		t.Fatalf("revision %v %v", new, err)
	}
	old, err := ts.GetNode(ids[0])
	if err != nil || old.State != "dismissed" {
		t.Fatal("revision lost historical state", err)
	}
	var oldPayload map[string]any
	_ = json.Unmarshal(old.Payload, &oldPayload)
	if oldPayload["summary"] != "same" {
		t.Fatal("history overwritten")
	}
	retry, again, err := ts.RecordFact(t.Context(), intent, replacement, nil, "worker", ids[0])
	if err != nil || again || retry != id {
		t.Fatal("revision retry not idempotent", err)
	}
	other, err := ts.AddNode(db.KindIntent, map[string]any{"summary": "another"}, 1, "running", "planner", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := ts.RecordFact(t.Context(), other, map[string]any{"summary": "wrong revision"}, nil, "worker", id); err == nil {
		t.Fatal("unrelated revision accepted")
	}
	if _, _, err := ts.RecordFact(t.Context(), intent, map[string]any{"summary": "invalid anchor"}, []int64{-1}, "worker", 0); err == nil {
		t.Fatal("invalid anchor accepted")
	}
	var leaked int
	if err := d.QueryRow(`SELECT count(*) FROM exploration_nodes WHERE exploration_id=$1 AND payload->>'summary'='invalid anchor'`, task.ExplorationID).Scan(&leaked); err != nil || leaked != 0 {
		t.Fatal("partial fact write", err)
	}
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, err := ts.RecordFact(cancelled, intent, map[string]any{"summary": "cancelled"}, nil, "worker", 0); err == nil {
		t.Fatal("cancel ignored")
	}
}

func TestRecordFactToolCoverageAndOwner(t *testing.T) {
	d := testDB(t)
	defer d.Close()
	task, err := d.CreateTask("coverage fact", "test", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(task.ID)
	ts := d.Exploration(task.ExplorationID)
	intent, err := ts.AddNode(db.KindIntent, map[string]any{"summary": "check"}, 1, "running", "planner", nil)
	if err != nil {
		t.Fatal(err)
	}
	asset, err := d.Assets().UpsertRootDomain(db.UpsertRootDomainReq{Domain: fmt.Sprintf("fact-%d.invalid", intent)})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Assets().DeleteByIDs([]int64{asset})
	tools := NewToolSet(ts, "worker")
	tools.ownerNode = intent
	item := factItem{Summary: "blocked", Outcome: "blocked", TestDimension: "read-only test", Evidence: "HTTP 403", RevisitCondition: "approved identity available"}
	if _, _, err := tools.recordFactItem(t.Context(), item, 0); err == nil {
		t.Fatal("missing tested assets accepted")
	}
	item.AssetIDs = []json.RawMessage{json.RawMessage(fmt.Sprint(asset))}
	first, created, err := tools.recordFactItem(t.Context(), item, 0)
	if err != nil || !created {
		t.Fatal(err)
	}
	second, created, err := tools.recordFactItem(t.Context(), item, 0)
	if err != nil || created || first != second || tools.Writes().Facts != 1 {
		t.Fatal("duplicate counted as progress", err)
	}
	var lineage int
	if err := d.QueryRow(`SELECT count(*) FROM exploration_edges WHERE src_id=$1 AND rel='yields' AND dst_id=$2`, intent, first).Scan(&lineage); err != nil || lineage != 1 {
		t.Fatal("owner intent not attached", err)
	}
	check := func(want int) {
		t.Helper()
		cov, err := d.Assets().TaskCoverageWithSources(task.ID)
		if err != nil || cov.Tested != want {
			t.Fatalf("coverage=%+v err=%v want=%d", cov, err, want)
		}
	}
	check(0)
	item.Summary = "observed negative"
	item.Outcome = "negative"
	item.Supersedes = first
	second, _, err = tools.recordFactItem(t.Context(), item, 0)
	if err != nil {
		t.Fatal(err)
	}
	check(1)
	if _, err := d.Exec(`UPDATE exploration_nodes SET state='dismissed' WHERE id=$1`, second); err != nil {
		t.Fatal(err)
	}
	check(0)
}

func TestFactWorkflowPreservesCustomSchemaAndAddsBatchFields(t *testing.T) {
	tool := NewToolSet(nil, "").recordFact()
	schema := obj(map[string]any{"custom_field": str("keep"), "facts": map[string]any{"type": "array", "items": obj(nil)}})
	tool = DecorateTool(tool, "stored description", schema)
	out := factWorkflowTools([]actool.CoreTool{tool})
	props := out[0].InputSchema()["properties"].(map[string]any)
	if props["outcome"] == nil || props["custom_field"] == nil {
		t.Fatal("runtime schema incomplete")
	}
	batch := props["facts"].(map[string]any)["items"].(map[string]any)["properties"].(map[string]any)
	if batch["supersedes"] == nil {
		t.Fatal("batch revision field missing")
	}
	if schema["properties"].(map[string]any)["outcome"] != nil {
		t.Fatal("shared schema mutated")
	}
}
