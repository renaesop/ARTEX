package db

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestFindingLifecycleReadsCurrentTriage(t *testing.T) {
	d, fid := retestDB(t)
	exp, err := d.CreateExploration("lifecycle test", "review")
	if err != nil {
		t.Fatal(err)
	}
	ts := d.Exploration(exp)
	nid, err := ts.AddNode(KindFinding, map[string]any{"summary": "historical success"}, 1, "confirmed", "worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = d.Exec(`DELETE FROM explorations WHERE id=$1`, exp) })
	if _, err := d.Exec(`UPDATE findings SET node_id=$1 WHERE id=$2`, nid, fid); err != nil {
		t.Fatal(err)
	}
	n, err := ts.GetNode(nid)
	if err != nil {
		t.Fatal(err)
	}
	before := string(n.Payload)
	for _, status := range []string{FindingFixed, FindingConfirmed, FindingFalsePositive, FindingResolved} {
		if _, err := d.Exec(`UPDATE findings SET status=$1 WHERE id=$2`, status, fid); err != nil {
			t.Fatal(err)
		}
		if err := ts.PopulateFindingTrafficIDs([]*Node{n}); err != nil {
			t.Fatal(err)
		}
		if n.FindingStatus != status || n.FindingID != fid || n.State != "confirmed" || string(n.Payload) != before {
			t.Fatalf("triage was stale or history rewritten: %+v", n)
		}
	}
	r, _, _, err := d.CreateFindingRetest(t.Context(), fid, "recheck")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`UPDATE finding_retests SET status='failed',verdict='inconclusive' WHERE id=$1`, r.ID); err != nil {
		t.Fatal(err)
	}
	if err := ts.PopulateFindingTrafficIDs([]*Node{n}); err != nil {
		t.Fatal(err)
	}
	var latest map[string]any
	if err := json.Unmarshal(n.LatestRetest, &latest); err != nil {
		t.Fatal(err)
	}
	if latest["status"] != "failed" || n.FindingStatus != FindingResolved || !strings.Contains(n.FindingLifecycleNote, "Historical evidence only") {
		t.Fatalf("failed retest changed triage: %+v", n)
	}
}
