package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
)

// RecordFact atomically writes a conclusion and its lineage. Exact retries within
// one intent return the existing ID without changing history or content versions.
// Superseding is explicit and stays within the same task and intent.
func (s *ExplorationStore) RecordFact(ctx context.Context, intentID int64, payload map[string]any, anchors []int64, origin string, supersedes int64) (int64, bool, error) {
	anchors = slices.Clone(anchors)
	slices.Sort(anchors)
	anchors = slices.Compact(anchors)
	if anchors == nil {
		anchors = []int64{}
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return 0, false, err
	}
	raw = jsonbClean(raw)
	anchorJSON, _ := json.Marshal(anchors)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, false, err
	}
	defer tx.Rollback()
	// Serialize all writers of this intent, including requests with reordered keys.
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, fmt.Sprintf("artex:fact:%d:%d", s.expID, intentID)); err != nil {
		return 0, false, err
	}
	if intentID > 0 {
		var valid bool
		err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM exploration_nodes WHERE id=$1 AND exploration_id=$2 AND kind='intent')`, intentID, s.expID).Scan(&valid)
		if err != nil {
			return 0, false, err
		}
		if !valid {
			return 0, false, fmt.Errorf("intent_id must belong to this task")
		}
	}
	var id int64
	err = tx.QueryRowContext(ctx, `SELECT n.id FROM exploration_nodes n
 WHERE n.exploration_id=$1 AND n.kind='fact' AND n.payload=$2::jsonb
 AND (($3::bigint=0 AND NOT EXISTS(SELECT 1 FROM exploration_edges e WHERE e.dst_id=n.id AND e.rel='yields'))
 OR EXISTS(SELECT 1 FROM exploration_edges e WHERE e.dst_id=n.id AND e.src_id=$3 AND e.rel='yields'))
 AND COALESCE((SELECT jsonb_agg(a.asset_id ORDER BY a.asset_id) FROM exploration_anchors a WHERE a.node_id=n.id),'[]'::jsonb)=$4::jsonb
 ORDER BY n.id LIMIT 1`, s.expID, string(raw), intentID, string(anchorJSON)).Scan(&id)
	if err == nil {
		return id, false, tx.Commit()
	}
	if err != sql.ErrNoRows {
		return 0, false, err
	}
	if supersedes > 0 {
		var valid bool
		err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM exploration_nodes n WHERE n.id=$1 AND n.exploration_id=$2 AND n.kind='fact' AND n.state='confirmed'
  AND EXISTS(SELECT 1 FROM exploration_edges e WHERE e.dst_id=n.id AND e.src_id=$3 AND e.rel='yields'))`, supersedes, s.expID, intentID).Scan(&valid)
		if err != nil {
			return 0, false, err
		}
		if !valid || intentID == 0 {
			return 0, false, fmt.Errorf("supersedes must be an active fact from this intent; inherited or unrelated facts are read-only")
		}
	}
	err = tx.QueryRowContext(ctx, `INSERT INTO exploration_nodes(exploration_id,kind,payload,priority,state,origin) VALUES($1,'fact',$2,5,'confirmed',$3) RETURNING id`, s.expID, string(raw), origin).Scan(&id)
	if err != nil {
		return 0, false, err
	}
	for _, a := range anchors {
		if _, err = tx.ExecContext(ctx, `INSERT INTO exploration_anchors(node_id,asset_id) VALUES($1,$2)`, id, a); err != nil {
			return 0, false, err
		}
	}
	if intentID > 0 {
		if _, err = tx.ExecContext(ctx, `INSERT INTO exploration_edges(exploration_id,src_id,rel,dst_id) VALUES($1,$2,'yields',$3)`, s.expID, intentID, id); err != nil {
			return 0, false, err
		}
	}
	if supersedes > 0 {
		if _, err = tx.ExecContext(ctx, `UPDATE exploration_nodes SET state='dismissed',content_version=content_version+1,updated_at=now() WHERE id=$1 AND exploration_id=$2`, supersedes, s.expID); err != nil {
			return 0, false, err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO exploration_edges(exploration_id,src_id,rel,dst_id) VALUES($1,$2,'derived_from',$3)`, s.expID, supersedes, id); err != nil {
			return 0, false, err
		}
	}
	return id, true, tx.Commit()
}
