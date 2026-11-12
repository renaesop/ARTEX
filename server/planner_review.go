package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/plannerreview"
)

// Explicit opt-in via the settings key planner_review:
// {"enabled":true,"profile_id":<existing OpenRouter profile ID>,"generation":0}.
// The profile supplies the key and proxy; Models defines the review model order.
// Generation is the operator's explicit re-review request for unchanged evidence.
// Disable the setting to restore ordinary heartbeat planning. No task is resumed
// by this setting. Due rechecks run on the next ordinary planner wake, not a new
// goroutine/timer; each unchanged snapshot permits at most one timed recheck.
type plannerReviewConfig struct {
	Enabled    bool  `json:"enabled"`
	ProfileID  int64 `json:"profile_id"`
	Generation int64 `json:"generation"`
}

func (e *Engine) reviewConfig() (plannerReviewConfig, bool) {
	var cfg plannerReviewConfig
	if e.m == nil || e.m.pg == nil {
		return cfg, false
	}
	raw, ok, err := e.m.pg.GetSetting("planner_review")
	if err != nil || !ok || json.Unmarshal([]byte(raw), &cfg) != nil || !cfg.Enabled || cfg.ProfileID <= 0 || cfg.Generation < 0 {
		return cfg, false
	}
	return cfg, true
}
func (c plannerReviewConfig) policy() string {
	return fmt.Sprintf("%s/profile:%d/generation:%d", plannerreview.PolicyVersion, c.ProfileID, c.Generation)
}

func (e *Engine) reviewEligible(ctx context.Context, t *Task) bool {
	return ctx.Err() == nil && !e.IsPaused(t.ID) && !e.IsDeleting(t.ID) && !e.isSettling(t.ID) && !isTerminalStatus(t.lifecycleSnapshot().Status)
}

// Called before ordinary planning, so the review's own audit events and ordinary
// heartbeats cannot start a planner/reviewer ping-pong on unchanged evidence.
func (e *Engine) skipReviewedIdle(ctx context.Context, t *Task) bool {
	cfg, on := e.reviewConfig()
	if !on || !e.reviewEligible(ctx, t) {
		return false
	}
	id, err := strconv.ParseInt(t.ID, 10, 64)
	if err != nil {
		return false
	}
	snap, err := e.m.pg.PlannerReviewSnapshot(ctx, id)
	if err != nil || snap.ActiveIntents != 0 || snap.OpenGoals == 0 {
		return false
	}
	seen, err := e.m.pg.PlannerReviewSeen(ctx, id, snap.Fingerprint, cfg.policy())
	return err == nil && seen
}

func (e *Engine) finishPlannerRound(ctx context.Context, t *Task, round plannerreview.Round, planErr error) {
	if e.m == nil || e.m.pg == nil || e.IsDeleting(t.ID) {
		return
	}
	taskID, err := strconv.ParseInt(t.ID, 10, 64)
	if err != nil {
		return
	}
	// Keep the end record even if Pause canceled the LLM. The admitted task
	// operation still holds the deletion barrier; never launch review on cancel.
	storeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	roundID, err := e.m.pg.SavePlannerRound(storeCtx, taskID, round, planErr != nil)
	cancel()
	if err != nil {
		log.Printf("[planner-review] task %s cannot save round: %v", t.ID, err)
		return
	}
	meta, _ := json.Marshal(map[string]any{"planner_round_id": roundID, "created_intent_ids": round.CreatedIntentIDs, "terminal_reason": round.TerminalReason, "zero_decision": round.Zero})
	e.emitActivity(t, db.Activity{Worker: "planner", Kind: "text", Summary: fmt.Sprintf("Round persisted: %d new intents; terminal=%s; structured zero reason=%t", len(round.CreatedIntentIDs), round.TerminalReason, round.Zero != nil), Metadata: meta})
	cfg, on := e.reviewConfig()
	if !on || planErr != nil || round.GoalMet || round.TerminalReason != "completed" || len(round.CreatedIntentIDs) > 0 || !e.reviewEligible(ctx, t) {
		return
	}
	deadline := time.Now().Add(5 * time.Minute)
	if unix := e.taskDeadline(t); unix > 0 && time.Unix(unix, 0).Before(deadline) {
		deadline = time.Unix(unix, 0)
	}
	rctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	snap, err := e.m.pg.PlannerReviewSnapshot(rctx, taskID)
	if err != nil || snap.OpenGoals == 0 || snap.ActiveIntents > 0 {
		return
	}
	reviewID, err := e.m.pg.ClaimPlannerReview(rctx, taskID, roundID, snap.Fingerprint, cfg.policy())
	if err != nil || reviewID == 0 {
		return
	}
	state := "interrupted"
	var nextCheck time.Time
	var results []plannerreview.Result
	var supplied []plannerreview.Evidence
	defer func() {
		finishCtx, done := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer done()
		payload := map[string]any{"round_id": roundID, "review_id": reviewID, "fingerprint": snap.Fingerprint, "state": state, "results": results, "evidence": supplied}
		if err := e.m.pg.FinishPlannerReview(finishCtx, reviewID, state, payload, nextCheck); err != nil {
			log.Printf("[planner-review] task %s cannot finish review: %v", t.ID, err)
		}
		if !e.IsDeleting(t.ID) {
			b, _ := json.Marshal(payload)
			e.emitActivity(t, db.Activity{Worker: "planner_review", Kind: "result", Summary: "Online zero-intent review: " + state, Detail: string(b), Metadata: json.RawMessage(fmt.Sprintf(`{"planner_review_id":%d}`, reviewID))})
		}
	}()
	profile, err := e.m.pg.ProfileByID(cfg.ProfileID)
	if err != nil || profile == nil || strings.TrimRight(profile.BaseURL, "/") != "https://openrouter.ai/api/v1" || strings.TrimSpace(profile.APIKey) == "" {
		state = "unavailable"
		return
	}
	e.emitActivity(t, db.Activity{Worker: "planner_review", Kind: "round", Summary: "Online zero-intent review; read-only; at most one evidence supplement"})
	// Model output is diagnostic data only: this path never dispatches work,
	// invokes the planner again, or changes goal/finding/task lifecycle states.
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil // match the planner profile's empty-proxy/direct behavior
	if proxy := strings.TrimSpace(profile.Proxy); proxy != "" {
		proxyURL, err := url.Parse(proxy)
		if err != nil || proxyURL.Host == "" || (proxyURL.Scheme != "http" && proxyURL.Scheme != "https" && proxyURL.Scheme != "socks5") {
			state = "unavailable"
			return
		}
		transport.Proxy = http.ProxyURL(proxyURL)
	}
	defer transport.CloseIdleConnections()
	client := &plannerreview.Client{APIKey: profile.APIKey, HTTP: &http.Client{Transport: transport}}
	if e.reviewClientForTest != nil {
		client = e.reviewClientForTest(profile.APIKey)
	}
	in := plannerreview.Input{RoundID: roundID, Round: round, Snapshot: snap}
	in.Round.FinalText = reviewText(in.Round.FinalText, 2000)
	record := func(a plannerreview.Attempt) error {
		c, done := context.WithTimeout(context.WithoutCancel(rctx), 3*time.Second)
		defer done()
		return e.m.pg.RecordPlannerReviewAttempt(c, reviewID, a)
	}
	check := func() bool {
		if !e.reviewEligible(rctx, t) {
			state = "canceled"
			return false
		}
		nowCfg, on := e.reviewConfig()
		if !on || nowCfg != cfg {
			state = "stale"
			return false
		}
		now, err := e.m.pg.PlannerReviewSnapshot(rctx, taskID)
		if err != nil {
			state = "unavailable"
			return false
		}
		if now.Fingerprint != snap.Fingerprint || now.ActiveIntents > 0 || now.OpenGoals == 0 {
			state = "stale"
			return false
		}
		return true
	}
	if !check() {
		return
	}
	e.BeginLLMCall(t.ID)
	r := client.Review(rctx, in, "", 1, record)
	e.EndLLMCall(t.ID)
	results = append(results, r)
	if !check() {
		return
	}
	if r.Decision == nil {
		state = reviewFailureState(r.Status)
		return
	}
	if !e.reviewCitationsValid(rctx, taskID, r.Decision) {
		state = "needs_attention"
		return
	}
	if r.Decision.Status == "needs_evidence" {
		for _, req := range r.Decision.Requests {
			v, err := e.m.pg.PlannerReviewEvidence(rctx, taskID, req)
			if err != nil {
				state = "unavailable"
				return
			}
			supplied = append(supplied, v)
		}
		if !check() {
			return
		}
		in.Previous = r.Decision
		in.Evidence = supplied
		e.BeginLLMCall(t.ID)
		r = client.Review(rctx, in, r.Model, 2, record)
		e.EndLLMCall(t.ID)
		results = append(results, r)
		if !check() {
			return
		}
		if r.Decision == nil {
			state = reviewFailureState(r.Status)
			return
		}
		if !e.reviewCitationsValid(rctx, taskID, r.Decision) {
			state = "needs_attention"
			return
		}
	}
	switch r.Decision.Status {
	case "supported_zero":
		state = "waiting_event"
	case "needs_evidence":
		state = "waiting_evidence"
	default:
		state = "needs_attention"
	}
	if state == "waiting_event" && r.Decision.RecheckAfterSeconds > 0 {
		nextCheck = time.Now().Add(time.Duration(r.Decision.RecheckAfterSeconds) * time.Second)
	}
}

func reviewFailureState(status string) string {
	switch status {
	case "refused", "policy_block":
		return "refused"
	case "canceled":
		return "canceled"
	default:
		return "unavailable"
	}
}
func reviewText(s string, max int) string {
	r := []rune(s)
	if len(r) > max {
		return string(r[:max]) + " [truncated]"
	}
	return s
}
func (e *Engine) reviewCitationsValid(ctx context.Context, taskID int64, d *plannerreview.Decision) bool {
	for _, id := range d.EvidenceIDs {
		// A citation must be scoped and actually available, not a hallucinated ID.
		ev, err := e.m.pg.PlannerReviewEvidence(ctx, taskID, plannerreview.EvidenceRequest{NodeID: id, Field: "summary"})
		if err != nil || ev.Unavailable {
			return false
		}
	}
	return true
}
