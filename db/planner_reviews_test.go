package db

import (
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/Autumn-27/artex/plannerreview"
)

func TestPlannerReviewSnapshotPersistenceAndIsolation(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err := d.EnsureLLMUsageTable(); err != nil {
		t.Fatal(err)
	}
	task, err := d.CreateTask("review snapshot", "audit scheduler", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(task.ID)
	ts := d.Exploration(task.ExplorationID)
	goal, err := ts.AddGoal(map[string]any{"text": "review evidence"}, "test")
	if err != nil {
		t.Fatal(err)
	}
	fact, err := ts.AddNode(KindFact, map[string]any{"summary": "recorded", "checkpoint": "continue reviewing archived records"}, 1, "confirmed", "test", nil)
	if err != nil {
		t.Fatal(err)
	}
	before, err := d.PlannerReviewSnapshot(t.Context(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if before.OpenGoals != 1 || before.ActiveIntents != 0 {
		t.Fatal(before)
	}
	// Audit writes and cold-graph bookkeeping must not trigger another paid call.
	_, err = ts.AppendActivity(Activity{Worker: "planner_review", Kind: "result", Summary: "done"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = d.Exec(`UPDATE exploration_nodes SET cold_since_round=99 WHERE id=$1`, fact); err != nil {
		t.Fatal(err)
	}
	after, err := d.PlannerReviewSnapshot(t.Context(), task.ID)
	if err != nil || before.Fingerprint != after.Fingerprint {
		t.Fatal("audit invalidated evidence", err)
	}
	other, err := d.CreateTask("private task", "other", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(other.ID)
	private, err := d.Exploration(other.ExplorationID).AddGoal(map[string]any{"text": "private text"}, "test")
	if err != nil {
		t.Fatal(err)
	}
	ev, err := d.PlannerReviewEvidence(t.Context(), task.ID, plannerreview.EvidenceRequest{NodeID: private, Field: "summary"})
	if err != nil || !ev.Unavailable {
		t.Fatal("cross-task disclosure", ev, err)
	}
	ev, err = d.PlannerReviewEvidence(t.Context(), task.ID, plannerreview.EvidenceRequest{NodeID: goal, Field: "summary"})
	if err != nil || ev.Unavailable {
		t.Fatal("goal text missing", err)
	}
	// Updating old payloads must invalidate even if a legacy writer forgets to bump content_version.
	if _, err = d.Exec(`UPDATE exploration_nodes SET payload=payload||'{"checkpoint":"new evidence"}' WHERE id=$1`, fact); err != nil {
		t.Fatal(err)
	}
	after, err = d.PlannerReviewSnapshot(t.Context(), task.ID)
	if err != nil || before.Fingerprint == after.Fingerprint {
		t.Fatal("content update ignored", err)
	}
	fn, err := ts.AddNode(KindFinding, map[string]any{"summary": "historical"}, 1, "confirmed", "test", nil)
	if err != nil {
		t.Fatal(err)
	}
	fid, err := d.AddFinding(task.ID, fn, "test", "test", "low", "summary", "evidence", "worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteFinding(fid)
	before, err = d.PlannerReviewSnapshot(t.Context(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = d.Exec(`UPDATE findings SET status='fixed' WHERE id=$1`, fid); err != nil {
		t.Fatal(err)
	}
	after, err = d.PlannerReviewSnapshot(t.Context(), task.ID)
	if err != nil || before.Fingerprint == after.Fingerprint {
		t.Fatal("triage update ignored", err)
	}
	rid, err := d.SavePlannerRound(t.Context(), task.ID, plannerreview.Round{TerminalReason: "completed"}, false)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	ids := make(chan int64, 8)
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id, err := d.ClaimPlannerReview(t.Context(), task.ID, rid, after.Fingerprint, "test")
			ids <- id
			errs <- err
		}()
	}
	wg.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	wins := 0
	var reviewID int64
	for id := range ids {
		if id > 0 {
			wins++
			reviewID = id
		}
	}
	if wins != 1 {
		t.Fatalf("claims won %d times", wins)
	}
	if _, err = d.Exec(`UPDATE planner_review_runs SET lease_until=now()-interval '1 minute' WHERE id=$1`, reviewID); err != nil {
		t.Fatal(err)
	}
	seen, err := d.PlannerReviewSeen(t.Context(), task.ID, after.Fingerprint, "test")
	if err != nil || !seen {
		t.Fatal("crash replay was not suppressed", err)
	}
	var state string
	if err = d.QueryRow(`SELECT state FROM planner_review_runs WHERE id=$1`, reviewID).Scan(&state); err != nil || state != "interrupted" {
		t.Fatal(state, err)
	}
	if id, err := d.ClaimPlannerReview(t.Context(), task.ID, rid, after.Fingerprint, "test"); id != 0 || err != nil {
		t.Fatal("reclaimed interrupted review", id, err)
	}
	// A documented timed prerequisite permits one recheck, with history kept.
	timerID, err := d.ClaimPlannerReview(t.Context(), task.ID, rid, after.Fingerprint, "timer")
	if err != nil {
		t.Fatal(err)
	}
	if err = d.FinishPlannerReview(t.Context(), timerID, "waiting_event", map[string]string{"reason": "cooldown"}, time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err = d.Exec(`UPDATE planner_review_runs SET next_check_at=now()-interval '1 second' WHERE id=$1`, timerID); err != nil {
		t.Fatal(err)
	}
	seen, err = d.PlannerReviewSeen(t.Context(), task.ID, after.Fingerprint, "timer")
	if err != nil || seen {
		t.Fatal("due timer still suppressed", err)
	}
	got, err := d.ClaimPlannerReview(t.Context(), task.ID, rid, after.Fingerprint, "timer")
	if err != nil || got != timerID {
		t.Fatal("timer not reclaimed", got, err)
	}
	if err = d.RecordPlannerReviewAttempt(t.Context(), timerID, plannerreview.Attempt{Model: "mock", Status: "completed", Phase: 1, Usage: &plannerreview.Usage{Input: 10, Output: 20}}); err != nil {
		t.Fatal(err)
	}
	if err = d.FinishPlannerReview(t.Context(), timerID, "waiting_event", nil, time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	var history, usageRows int
	var next any
	if err = d.QueryRow(`SELECT jsonb_array_length(history),next_check_at FROM planner_review_runs WHERE id=$1`, timerID).Scan(&history, &next); err != nil || history != 1 || next != nil {
		t.Fatal("unbounded timer or lost history", history, next, err)
	}
	if err = d.QueryRow(`SELECT count(*) FROM llm_usage WHERE task_id=$1 AND worker='planner_review'`, strconv.FormatInt(task.ID, 10)).Scan(&usageRows); err != nil || usageRows != 1 {
		t.Fatal("attempt not metered", usageRows, err)
	}
}

func TestPlannerReviewPrerequisiteChanges(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	task, err := d.CreateTask("review prerequisites", "audit", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(task.ID)
	snapshot := func() string {
		t.Helper()
		s, err := d.PlannerReviewSnapshot(t.Context(), task.ID)
		if err != nil {
			t.Fatal(err)
		}
		return s.Fingerprint
	}
	before := snapshot()
	if _, err = d.Exec(`INSERT INTO task_credentials(task_id,domain,username,password) VALUES($1,'example.invalid','test','placeholder')`, task.ID); err != nil {
		t.Fatal(err)
	}
	if snapshot() == before {
		t.Fatal("credential prerequisite change ignored")
	}
	before = snapshot()
	if _, err = d.Exec(`INSERT INTO task_scope(task_id,kind,domain,source) VALUES($1,'subdomain','review.example.invalid','manual')`, task.ID); err != nil {
		t.Fatal(err)
	}
	if snapshot() == before {
		t.Fatal("scope change ignored")
	}
	before = snapshot()
	var assetID int64
	if err = d.QueryRow(`INSERT INTO assets(type,domain) VALUES('subdomain','review.example.invalid') RETURNING id`).Scan(&assetID); err != nil {
		t.Fatal(err)
	}
	defer d.Exec(`DELETE FROM assets WHERE id=$1`, assetID)
	if snapshot() == before {
		t.Fatal("new asset in scope ignored")
	}
	before = snapshot()
	if _, err = d.Exec(`UPDATE assets SET last_seen=now() WHERE id=$1`, assetID); err != nil {
		t.Fatal(err)
	}
	if snapshot() != before {
		t.Fatal("asset timestamp invalidated evidence")
	}
	if _, err = d.Exec(`UPDATE assets SET page_title='new evidence' WHERE id=$1`, assetID); err != nil {
		t.Fatal(err)
	}
	if snapshot() == before {
		t.Fatal("asset content update ignored")
	}
}
