package agent

import (
	"encoding/json"
	actool "github.com/Autumn-27/norma/tool"
)

const coverageCaveat = "This is an asset-level fact-anchor index, not a complete test ledger. Missing anchors do not prove an asset was never checked; an anchor does not prove all dimensions were checked. Legacy facts lack structured outcomes. Blocked/incomplete and dismissed facts do not mark an asset tested. Read prior conclusions, dimensions and revisit conditions before dispatching work."

// Appended after editable prompts: old stored prompts cannot reinstate forced
// dispatch or encourage duplicate persistence.
const progressContract = `

Execution progress policy (applies even if an earlier editable prompt says otherwise):
- Keep the existing role boundary: workers execute only their assigned intent; only the planner dispatches directions. These rules do not expand scope or authorize bypassing constraints.
- Reuse existing evidence and checkpoints. Write one consolidated fact per conclusion; an unchanged retry returns its existing ID. Do not rewrite summaries as new facts or claim a new summary silently replaces old evidence. Use supersedes for an explicit complete revision of a fact from the same intent.
- For structured coverage record outcome (verified/negative/blocked/incomplete), the specific test_dimension, explicit actually-tested asset_ids, and evidence. A timeout/403/missing credential is blocked or incomplete, not proof of absence. A historical success is not current access; check lifecycle and retest state.
- Record a concrete revisit_condition for a blocked direction and checkpoint for incomplete work. The planner must, before dispatching a similar intent, cite the prior conclusion and new evidence that changes its prerequisite. Renaming a direction, using another alias or changing the wording is not new evidence.
- Planner only: continue advancing an unmet goal when a concrete, authorized, non-duplicate next step is executable. Zero new intents is appropriate only when existing work already covers the next steps, the goal is met, or all remaining known directions have explicit unmet prerequisites. In the last case, state the prior evidence, the missing prerequisite and the concrete event that would permit resuming. Do not treat lack of an immediate idea as evidence that all directions are blocked.
- Budget exhaustion is incomplete work, not a negative result or blocker. On the next authorized continuation, read its checkpoint and continue the remaining steps without replaying completed work. Only wait if the remaining steps themselves have a specific unmet prerequisite.
- Repeated identical graph reads with unchanged results enter bounded settlement. Read available output once, page with the returned cursor, and stop polling unchanged graph data. During settlement persist only already-observed results and checkpoints, then stop.
- Coverage is a partial asset-anchor index. Missing anchors mean missing records, not "never tested"; a recorded test covers its stated dimension only.
`

func factWorkflowTools(tools []actool.CoreTool) []actool.CoreTool {
	out := append([]actool.CoreTool(nil), tools...)
	for i, t := range out {
		if t.Name() == "list_untested_assets" {
			out[i] = DecorateTool(t, t.Description()+"\n"+coverageCaveat, nil)
		}
		if t.Name() != "record_fact" {
			continue
		}
		// Clone rather than mutating shared schemas. Preserve DB bindings, defaults
		// and custom properties; only add application-owned optional fields.
		raw, _ := json.Marshal(t.InputSchema())
		var schema map[string]any
		_ = json.Unmarshal(raw, &schema)
		if schema == nil {
			schema = map[string]any{"type": "object"}
		}
		props, _ := schema["properties"].(map[string]any)
		if props == nil {
			props = map[string]any{}
			schema["properties"] = props
		}
		extra := map[string]any{
			"supersedes":        intp("active fact ID from this same intent to replace; retains old evidence"),
			"outcome":           str("verified | negative | blocked | incomplete; requires test_dimension, evidence and explicit tested asset_ids"),
			"test_dimension":    str("exact check and relevant identity/environment, not whole-host coverage"),
			"revisit_condition": str("concrete changed prerequisite or new evidence needed before reopening"),
			"checkpoint":        str("completed steps, artifact references and remaining work; omit secrets"),
		}
		for k, v := range extra {
			props[k] = v
		}
		if facts, ok := props["facts"].(map[string]any); ok {
			itemSchema, ok := facts["items"].(map[string]any)
			if !ok {
				itemSchema = map[string]any{"type": "object"}
				facts["items"] = itemSchema
			}
			itemProps, ok := itemSchema["properties"].(map[string]any)
			if !ok {
				itemProps = map[string]any{}
				itemSchema["properties"] = itemProps
			}
			for k, v := range props {
				if k != "facts" {
					if _, exists := itemProps[k]; !exists {
						itemProps[k] = v
					}
				}
			}
			for k, v := range extra {
				itemProps[k] = v
			}

		}
		out[i] = DecorateTool(t, t.Description()+"\nExact retries within the same intent return the existing fact ID; do not retry after success. Use supersedes for a complete revision, retaining history. Structured outcomes apply only to the explicitly listed assets and dimension.", schema)
	}
	return out
}
