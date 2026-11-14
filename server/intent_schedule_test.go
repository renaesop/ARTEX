package server

import (
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
)

func TestEngineClaimsReadyWorkBehindDelayedFrontier(t *testing.T) {
	dsn, _, err := db.DSN()
	if err != nil {
		t.Skip(err)
	}
	d, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	exp, err := d.CreateExploration("delayed engine claim", "")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Exec(`DELETE FROM explorations WHERE id=$1`, exp)
	s := d.Exploration(exp)
	for range 25 {
		if _, err := s.AddIntent(map[string]any{"summary": "not due", "not_before": time.Now().Add(time.Hour).Format(time.RFC3339)}, 10, nil, "planner"); err != nil {
			t.Fatal(err)
		}
	}
	id, err := s.AddIntent(map[string]any{"summary": "due now"}, 1, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	e := NewEngine(nil)
	task := &Task{Store: s}
	if n := e.claimNext(task, "worker"); n == nil || n.ID != id {
		t.Fatalf("ready work starved: %+v", n)
	}
	if n := e.claimNext(task, "worker"); n != nil {
		t.Fatal("claimed delayed work", n.ID)
	}
}
