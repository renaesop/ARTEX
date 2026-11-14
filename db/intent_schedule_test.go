package db

import (
	"testing"
	"time"
)

func TestIntentScheduleValidation(t *testing.T) {
	for _, raw := range []any{"tomorrow", "2026-11-14T11:12:00", "", nil, 123} {
		if _, err := normalizeIntentSchedule(map[string]any{"not_before": raw}); err == nil {
			t.Fatalf("accepted %v", raw)
		}
	}
	p := map[string]any{"not_before": "2026-11-14T11:12:00.5+08:00", "not_before_unix": 0}
	n, err := normalizeIntentSchedule(p)
	if err != nil {
		t.Fatal(err)
	}
	due, _ := time.Parse(time.RFC3339Nano, "2026-11-14T03:12:00.5Z")
	if n["not_before_unix"] != due.Unix()+1 || n["not_before"] != "2026-11-14T03:12:00.5Z" {
		t.Fatal(n)
	}
	if p["not_before_unix"] != 0 {
		t.Fatal("mutated caller")
	}
}

func TestDelayedIntentCannotBeClaimedOrStarveReadyWork(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	exp, err := d.CreateExploration("schedule regression", "")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Exec(`DELETE FROM explorations WHERE id=$1`, exp)
	s := d.Exploration(exp)
	var delayed int64
	for range 25 {
		delayed, err = s.AddIntent(map[string]any{"summary": "future observation", "not_before": time.Now().Add(time.Hour).Format(time.RFC3339)}, 10, nil, "planner")
		if err != nil {
			t.Fatal(err)
		}
	}
	ready, err := s.AddIntent(map[string]any{"summary": "ready now"}, 1, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	fr, err := s.ReadyFrontier(1)
	if err != nil || len(fr) != 1 || fr[0].ID != ready {
		t.Fatalf("ready frontier %v %v", fr, err)
	}
	if ok, err := s.ClaimIntent(delayed, "worker"); err != nil || ok {
		t.Fatalf("claimed early: %v %v", ok, err)
	}
	if n, err := s.CountOpenIntents(); err != nil || n != 26 {
		t.Fatalf("pending lost: %d %v", n, err)
	}
	if active, err := s.HasActiveIntent(); err != nil || !active {
		t.Fatal("delayed work must keep task active", err)
	}
	if n, err := s.CountReadyIntents(); err != nil || n != 1 {
		t.Fatalf("ready count: %d %v", n, err)
	}
	// Simulate time becoming due without sleeping or involving a model.
	_, err = d.Exec(`UPDATE exploration_nodes SET payload=jsonb_set(payload,'{not_before_unix}',to_jsonb(extract(epoch FROM now())-1)) WHERE id=$1`, delayed)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := s.ClaimIntent(delayed, "worker"); err != nil || !ok {
		t.Fatalf("not claimed when due: %v %v", ok, err)
	}
	if ok, _ := s.ClaimIntent(delayed, "other"); ok {
		t.Fatal("double claimed")
	}
	// Imported malformed schedules fail closed without breaking the whole queue.
	_, err = d.Exec(`UPDATE exploration_nodes SET payload=payload || '{"not_before":"bad","not_before_unix":"bad"}'::jsonb WHERE id=$1`, ready)
	if err != nil {
		t.Fatal(err)
	}
	if fr, err = s.ReadyFrontier(20); err != nil || len(fr) != 0 {
		t.Fatalf("malformed schedule %v %v", fr, err)
	}
}
