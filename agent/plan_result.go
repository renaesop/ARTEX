package agent

import (
	"context"
	"encoding/json"
	"sort"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/plannerreview"
	actool "github.com/Autumn-27/norma/tool"
)

// Plan preserves the old API for the timeout coordinator and other callers.
// Ordinary scheduling uses PlanRound so zero-production evidence is not lost.
func (p *Planner) Plan(ctx context.Context, taskID int64, as *db.AssetStore, ts *db.ExplorationStore, goal string, triggers []TriggerEvent, emit func(db.Activity)) (bool, string, error) {
	r, err := p.PlanRound(ctx, taskID, as, ts, goal, triggers, emit)
	return r.GoalMet, r.GoalReason, err
}

func (t *ToolSet) plannerOutcome() ([]int64, *plannerreview.ZeroDecision) {
	t.planMu.Lock()
	defer t.planMu.Unlock()
	ids := append([]int64{}, t.createdIntents...)
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	if t.zeroDecision == nil || len(ids) > 0 {
		return ids, nil
	}
	d := *t.zeroDecision
	d.EvidenceIDs = append([]int64{}, d.EvidenceIDs...)
	return ids, &d
}

func (t *ToolSet) finishPlanning() actool.CoreTool {
	return t.readExpTool("finish_planning", "Record this round's zero-intent decision. Does not finish the task or create work. Cite existing evidence, state the blocker and a concrete condition for reevaluation. Do not equate budget exhaustion or missing ideas with exhausted coverage.",
		obj(map[string]any{"reason": str("covered | waiting_dependency | budget_exhausted | insufficient_evidence"),
			"evidence_ids":     map[string]any{"type": "array", "items": map[string]string{"type": "integer"}},
			"explanation":      str("evidence-based explanation for producing zero new intents"),
			"resume_condition": str("specific external event or evidence needed to reconsider")}),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			if err := ctx.Err(); err != nil {
				return actool.Errorf("planning canceled"), nil
			}
			var d plannerreview.ZeroDecision
			if json.Unmarshal(in, &d) != nil {
				return actool.Errorf("invalid decision JSON"), nil
			}
			if err := d.Validate(); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			for _, id := range d.EvidenceIDs {
				n, err := t.ts.GetNodeWithSources(id)
				if err != nil || n == nil {
					return actool.Errorf("evidence must belong to this task or its direct sources"), nil
				}
			}
			t.planMu.Lock()
			t.zeroDecision = &d
			t.planMu.Unlock()
			return actool.Text("Zero-intent explanation recorded for this round; task state unchanged."), nil
		})
}
