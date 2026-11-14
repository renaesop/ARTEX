package db

import (
	"fmt"
	"time"
)

// Scheduling lives in the node payload so archives and checkpoints preserve it.
// not_before_unix is derived by the application, never accepted from callers.
func normalizeIntentSchedule(payload map[string]any) (map[string]any, error) {
	out := make(map[string]any, len(payload)+1)
	for k, v := range payload {
		out[k] = v
	}
	delete(out, "not_before_unix")
	raw, present := payload["not_before"]
	if !present {
		return out, nil
	}
	s, ok := raw.(string)
	if !ok || s == "" {
		return nil, fmt.Errorf("not_before must be an RFC3339 timestamp with timezone")
	}
	due, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return nil, fmt.Errorf("not_before must be an RFC3339 timestamp with timezone: %w", err)
	}
	// Round up: fractional timestamps must never become eligible early.
	sec := due.Unix()
	if due.Nanosecond() != 0 {
		sec++
	}
	out["not_before"] = due.UTC().Format(time.RFC3339Nano)
	out["not_before_unix"] = sec
	return out, nil
}

// JSONB comparisons avoid unsafe casts on imported/malformed metadata. An
// incomplete or invalid schedule remains unclaimable rather than running early.
const intentReadySQL = `(NOT (payload ? 'not_before') OR
 (jsonb_typeof(payload->'not_before_unix') = 'number'
 AND payload->'not_before_unix' <= to_jsonb(extract(epoch FROM now()))))`

// ReadyFrontier filters before LIMIT so delayed high-priority entries cannot
// starve ordinary work. Frontier still includes them for UI/planner visibility.
func (s *ExplorationStore) ReadyFrontier(limit int) ([]*Node, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.Query(`SELECT `+nodeCols+` FROM exploration_nodes
 WHERE exploration_id=$1 AND kind='intent' AND state='open' AND `+intentReadySQL+`
 ORDER BY priority DESC,id ASC LIMIT $2`, s.expID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanNodes(rows)
}

func (s *ExplorationStore) CountReadyIntents() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT count(*) FROM exploration_nodes WHERE exploration_id=$1
 AND kind='intent' AND state='open' AND `+intentReadySQL, s.expID).Scan(&n)
	return n, err
}

// HasRunnableOrRunningIntent reports whether workers can make progress now.
// HasActiveIntent also includes delayed work and is used when deciding completion.
func (s *ExplorationStore) HasRunnableOrRunningIntent() (bool, error) {
	var exists bool
	err := s.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM exploration_nodes
WHERE exploration_id=$1 AND kind='intent'
AND (state='running' OR (state='open' AND `+intentReadySQL+`)))`, s.expID).Scan(&exists)
	return exists, err
}
