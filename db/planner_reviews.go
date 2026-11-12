package db

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Autumn-27/artex/plannerreview"
)

// Review scope is the task and its explicitly linked, live direct sources only.
const reviewScope = `WITH scope AS (
 SELECT id AS task_id, exploration_id FROM tasks WHERE id=$1 AND deleted_at IS NULL
 UNION SELECT s.id,s.exploration_id FROM task_relations r JOIN tasks s ON s.id=r.source_task_id
 WHERE r.task_id=$1 AND s.deleted_at IS NULL
) `

// PlannerReviewSnapshot reads counts, versions and a bounded projection in ONE
// repeatable-read transaction. Activity/review logs, heartbeat timestamps and
// cold-graph bookkeeping never invalidate the fingerprint.
func (d *DB) PlannerReviewSnapshot(ctx context.Context, taskID int64) (plannerreview.Snapshot, error) {
	var out plannerreview.Snapshot
	tx, err := d.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	var exp int64
	if err = tx.QueryRowContext(ctx, `SELECT exploration_id FROM tasks WHERE id=$1 AND deleted_at IS NULL`, taskID).Scan(&exp); err != nil {
		return out, err
	}
	err = tx.QueryRowContext(ctx, `SELECT count(*) FILTER(WHERE kind='goal' AND state='open'),count(*) FILTER(WHERE kind='intent' AND state IN ('open','running')),count(*) FROM exploration_nodes WHERE exploration_id=$1`, exp).Scan(&out.OpenGoals, &out.ActiveIntents, &out.TotalNodes)
	if err != nil {
		return out, err
	}
	// Hash all relevant rows, not the truncated presentation window. Standalone
	// finding triage and retests change independently of exploration node versions.
	queries := []string{
		reviewScope + `SELECT COALESCE(jsonb_agg(to_jsonb(c)-'created_at' ORDER BY c.id)::text,'[]') FROM task_scope c JOIN scope s ON s.task_id=c.task_id`,
		reviewScope + `SELECT COALESCE(jsonb_agg(to_jsonb(c)-'created_at'-'updated_at' ORDER BY c.id)::text,'[]') FROM task_credentials c JOIN scope s ON s.task_id=c.task_id`,
		reviewScope + `SELECT COALESCE(jsonb_agg(to_jsonb(c)-'created_at'-'updated_at' ORDER BY c.id)::text,'[]') FROM task_intercept_rules c JOIN scope s ON s.task_id=c.task_id`,
		`WITH ` + contextCoverageCTE + ` SELECT COALESCE(jsonb_agg(to_jsonb(a)-'created_at'-'updated_at'-'last_seen'-'task_ids' ORDER BY a.id)::text,'[]') FROM assets a WHERE a.id IN (SELECT id FROM target) OR a.id IN (SELECT asset_id FROM task_asset_links WHERE task_id IN (SELECT task_id FROM context_tasks))`,
		reviewScope + `SELECT COALESCE(jsonb_agg(jsonb_build_array(t.id,t.goal,t.description,t.paused_at,t.timeout_seconds,t.coverage_enabled) ORDER BY t.id)::text,'[]') FROM tasks t JOIN scope s ON s.task_id=t.id`,
		reviewScope + `SELECT COALESCE(jsonb_agg(jsonb_build_array(n.id,n.kind,n.state,n.payload,n.blocked_reason,n.content_version) ORDER BY n.id)::text,'[]') FROM exploration_nodes n JOIN scope s ON s.exploration_id=n.exploration_id WHERE n.kind NOT IN ('digest','begin')`,
		reviewScope + `SELECT COALESCE(jsonb_agg(to_jsonb(c) ORDER BY c.id)::text,'[]') FROM task_constraints c JOIN scope s ON s.exploration_id=c.exploration_id`,
		reviewScope + `SELECT COALESCE(jsonb_agg(to_jsonb(e) ORDER BY e.src_id,e.rel,e.dst_id)::text,'[]') FROM exploration_edges e JOIN scope s ON s.exploration_id=e.exploration_id WHERE e.rel<>'covers'`,
		reviewScope + `SELECT COALESCE(jsonb_agg(jsonb_build_array(f.id,f.status,f.evidence_version,f.report_evidence_version) ORDER BY f.id)::text,'[]') FROM findings f WHERE f.task_id IN (SELECT task_id FROM scope) OR f.node_id IN (SELECT id FROM exploration_nodes WHERE exploration_id IN (SELECT exploration_id FROM scope))`,
		reviewScope + `SELECT COALESCE(jsonb_agg(jsonb_build_array(r.id,r.status,r.verdict,r.finished_at,r.summary) ORDER BY r.id)::text,'[]') FROM finding_retests r JOIN findings f ON f.id=r.finding_id WHERE f.task_id IN (SELECT task_id FROM scope) OR f.node_id IN (SELECT id FROM exploration_nodes WHERE exploration_id IN (SELECT exploration_id FROM scope))`,
		reviewScope + `SELECT COALESCE(jsonb_agg(jsonb_build_array(a.id,a.node_id,a.is_error,md5(a.detail)) ORDER BY a.id)::text,'[]') FROM activity a JOIN scope s ON s.exploration_id=a.exploration_id WHERE a.kind='result' AND a.node_id IS NOT NULL AND a.worker NOT IN ('planner','planner_review')`,
	}
	h := sha256.New()
	for _, q := range queries {
		var raw string
		// Aggregate hashes inside PG so large raw traces/credentials are never
		// returned to the reviewer or held in application logs.
		q = "SELECT md5(v) FROM (" + strings.TrimSuffix(q, ";") + ") AS digest_source(v)"
		if err = tx.QueryRowContext(ctx, q, taskID).Scan(&raw); err != nil {
			return out, err
		}
		h.Write([]byte(raw))
		h.Write([]byte{0})
	}
	out.Fingerprint = hex.EncodeToString(h.Sum(nil))
	out.CapturedAt = time.Now().UTC()
	rows, err := tx.QueryContext(ctx, reviewScope+`SELECT n.id,n.kind,n.state,left(COALESCE(n.payload->>'text',n.payload->>'summary',''),800),COALESCE(f.status,''),COALESCE((SELECT verdict FROM finding_retests WHERE finding_id=f.id ORDER BY id DESC LIMIT 1),'')
 FROM exploration_nodes n JOIN scope s ON s.exploration_id=n.exploration_id
 LEFT JOIN LATERAL (SELECT id,status FROM findings WHERE node_id=n.id ORDER BY id DESC LIMIT 1) f ON true
 WHERE n.kind IN ('goal','intent','fact','finding','hint') AND (s.task_id=$1 OR n.kind<>'intent' OR n.state IN ('done','blocked','exhausted','stopped'))
 ORDER BY (n.kind='goal') DESC,n.id DESC LIMIT 81`, taskID)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var n plannerreview.Node
		if err = rows.Scan(&n.ID, &n.Kind, &n.State, &n.Summary, &n.FindingStatus, &n.RetestVerdict); err != nil {
			rows.Close()
			return out, err
		}
		out.Nodes = append(out.Nodes, n)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	if len(out.Nodes) > 80 {
		out.Truncated = true
		out.Nodes = out.Nodes[:80]
	}
	rows, err = tx.QueryContext(ctx, reviewScope+`SELECT c.kind||': '||left(c.text,1000) FROM task_constraints c JOIN scope s ON s.exploration_id=c.exploration_id ORDER BY c.id LIMIT 31`, taskID)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var s string
		if err = rows.Scan(&s); err != nil {
			rows.Close()
			return out, err
		}
		out.Constraints = append(out.Constraints, s)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	if len(out.Constraints) > 30 {
		return out, errors.New("review constraints exceed bounded context")
	}
	return out, tx.Commit()
}

// PlannerReviewEvidence has no arbitrary SQL/path/command input. Only explicit
// fields from scoped nodes or their existing terminal worker records are read.
func (d *DB) PlannerReviewEvidence(ctx context.Context, taskID int64, r plannerreview.EvidenceRequest) (plannerreview.Evidence, error) {
	out := plannerreview.Evidence{Request: r, Unavailable: true}
	if !r.Valid() {
		return out, errors.New("invalid evidence request")
	}
	var value sql.NullString
	q := reviewScope + `SELECT CASE WHEN $3='blocked_reason' THEN n.blocked_reason WHEN $3='summary' THEN COALESCE(n.payload->>'text',n.payload->>'summary') ELSE n.payload->>$3 END FROM exploration_nodes n JOIN scope s ON s.exploration_id=n.exploration_id WHERE n.id=$2 AND (s.task_id=$1 OR n.kind<>'intent' OR n.state IN ('done','blocked','exhausted','stopped'))`
	if r.Field == "worker_result" {
		q = reviewScope + `SELECT a.detail FROM activity a JOIN exploration_nodes n ON n.id=a.node_id JOIN scope s ON s.exploration_id=n.exploration_id WHERE n.id=$2 AND a.exploration_id=n.exploration_id AND a.kind='result' AND a.worker NOT IN ('planner','planner_review') AND n.state IN ('done','blocked','exhausted','stopped') AND $3='worker_result' ORDER BY a.id DESC LIMIT 1`
	}
	if r.Field == "lineage" {
		q = reviewScope + `SELECT (SELECT jsonb_agg(jsonb_build_object('from',e.src_id,'to',e.dst_id,'rel',e.rel))::text FROM (SELECT src_id,dst_id,rel FROM exploration_edges WHERE exploration_id=n.exploration_id AND (src_id=n.id OR dst_id=n.id) AND rel<>'covers' ORDER BY src_id,rel,dst_id LIMIT 30) e) FROM exploration_nodes n JOIN scope s ON s.exploration_id=n.exploration_id WHERE n.id=$2 AND $3='lineage' AND (s.task_id=$1 OR n.kind<>'intent' OR n.state IN ('done','blocked','exhausted','stopped'))`
	}
	err := d.QueryRowContext(ctx, q, taskID, r.NodeID, r.Field).Scan(&value)
	if err == sql.ErrNoRows {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	if value.Valid && strings.TrimSpace(value.String) != "" {
		out.Value = reviewClip(value.String, 4000)
		out.Unavailable = false
	}
	return out, nil
}

func reviewClip(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + " [truncated]"
	}
	return s
}

func (d *DB) SavePlannerRound(ctx context.Context, taskID int64, r plannerreview.Round, failed bool) (int64, error) {
	b, err := json.Marshal(r)
	if err != nil {
		return 0, err
	}
	var id int64
	err = d.QueryRowContext(ctx, `INSERT INTO planner_round_results(task_id,result,failed) VALUES($1,$2,$3) RETURNING id`, taskID, jsonbClean(b), failed).Scan(&id)
	return id, err
}

// A persisted claim suppresses all automatic repeats, including after a crash.
// Expired running claims become interrupted; they are NOT re-acquired and billed
// again. New evidence or an explicit configuration generation permits a review.
func (d *DB) PlannerReviewSeen(ctx context.Context, taskID int64, fp, policy string) (bool, error) {
	_, err := d.ExecContext(ctx, `UPDATE planner_review_runs SET state='interrupted',finished_at=now() WHERE task_id=$1 AND fingerprint=$2 AND policy=$3 AND state='running' AND lease_until<now()`, taskID, fp, policy)
	if err != nil {
		return false, err
	}
	var exists bool
	err = d.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM planner_review_runs WHERE task_id=$1 AND fingerprint=$2 AND policy=$3 AND NOT(state='waiting_event' AND rechecks=0 AND next_check_at IS NOT NULL AND next_check_at<=now()))`, taskID, fp, policy).Scan(&exists)
	return exists, err
}
func (d *DB) ClaimPlannerReview(ctx context.Context, taskID, roundID int64, fp, policy string) (int64, error) {
	var id int64
	err := d.QueryRowContext(ctx, `INSERT INTO planner_review_runs(task_id,round_id,fingerprint,policy,state,lease_until) VALUES($1,$2,$3,$4,'running',now()+interval '6 minutes')
 ON CONFLICT(task_id,fingerprint,policy) DO UPDATE SET
 history=planner_review_runs.history||jsonb_build_array(jsonb_build_object('round_id',planner_review_runs.round_id,'result',planner_review_runs.result,'attempts',planner_review_runs.attempts,'finished_at',planner_review_runs.finished_at)),
 round_id=EXCLUDED.round_id,state='running',lease_until=EXCLUDED.lease_until,rechecks=planner_review_runs.rechecks+1,
 result=NULL,attempts='[]',finished_at=NULL,next_check_at=NULL
 WHERE planner_review_runs.state='waiting_event' AND planner_review_runs.rechecks=0 AND planner_review_runs.next_check_at<=now()
 RETURNING id`, taskID, roundID, fp, policy).Scan(&id)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return id, err
}
func (d *DB) RecordPlannerReviewAttempt(ctx context.Context, id int64, a plannerreview.Attempt) error {
	b, err := json.Marshal(a)
	if err != nil {
		return err
	}
	var input, output, cached, written int
	if a.Usage != nil {
		input, output, cached, written = a.Usage.Input, a.Usage.Output, a.Usage.InputDetails.Cached, a.Usage.InputDetails.Written
	}
	status := "error"
	if a.Status == "completed" {
		status = "ok"
	}
	// Use a distinct unpriced profile bucket: the key's profile may be priced
	// for DeepSeek or another model. Actual USD cost remains in attempts.usage.
	r, err := d.ExecContext(ctx, `WITH updated AS (
 UPDATE planner_review_runs SET attempts=attempts||jsonb_build_array($2::jsonb) WHERE id=$1 AND state='running' RETURNING task_id
 ) INSERT INTO llm_usage(task_id,exploration_id,worker,model,profile_name,latency_ms,input_tokens,output_tokens,cache_read,cache_write,status)
 SELECT t.id::text,t.exploration_id,'planner_review',$3,'planner_review/'||$3,$4,$5,$6,$7,$8,$9 FROM updated u JOIN tasks t ON t.id=u.task_id`, id, jsonbClean(b), a.Model, a.LatencyMS, input, output, cached, written, status)
	if err != nil {
		return err
	}
	n, _ := r.RowsAffected()
	if n != 1 {
		return errors.New("review no longer active")
	}
	return nil
}
func (d *DB) FinishPlannerReview(ctx context.Context, id int64, state string, result any, nextCheck ...time.Time) error {
	switch state {
	case "waiting_evidence", "waiting_event", "needs_attention", "refused", "unavailable", "canceled", "stale", "interrupted":
	default:
		return fmt.Errorf("invalid review state %q", state)
	}
	b, err := json.Marshal(result)
	if err != nil {
		return err
	}
	var wake any
	if state == "waiting_event" && len(nextCheck) == 1 && nextCheck[0].After(time.Now()) && nextCheck[0].Before(time.Now().Add(25*time.Hour)) {
		wake = nextCheck[0]
	}
	_, err = d.ExecContext(ctx, `UPDATE planner_review_runs SET state=$2,result=$3,finished_at=now(),next_check_at=CASE WHEN rechecks=0 THEN $4::timestamptz ELSE NULL END WHERE id=$1 AND state='running'`, id, state, jsonbClean(b), wake)
	return err
}
