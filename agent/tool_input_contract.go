package agent

import "encoding/json"

func factItemSchema() map[string]any {
	schema := obj(map[string]any{
		"summary":    str("a [one-sentence summary] of this exploration's conclusion (a condensation of detail)"),
		"intent_id":  idp("the id of the intent that produced this fact (the intent you claimed; the default for each item in batch mode)"),
		"detail":     str("details related to this fact: write all of this exploration's observations here"),
		"evidence":   str("[one line] of key evidence: the command + the one or two most proving output lines. Keep it brief; don't paste large output blocks (details go in detail)."),
		"confidence": str("observed (seen directly in output) | inferred (inferred from phenomena). Always label negative conclusions honestly."),
		"asset_ids":  map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "related asset ids (optional, 0/1/many): which assets this fact involves"},
	}, "summary")
	summary := schema["properties"].(map[string]any)["summary"].(map[string]any)
	summary["minLength"] = 1
	return schema
}

func recordFactSchema() map[string]any {
	item := factItemSchema()
	props := factItemSchema()["properties"].(map[string]any)
	props["facts"] = map[string]any{
		"type": "array", "minItems": 1, "items": item,
		"description": "Batch mode: non-empty array of complete facts. Every item requires its own summary. Only intent_id inherits from the top level. Returned ids match item order; retry only failed indices.",
	}
	schema := obj(props)
	// Preserve both existing call shapes without requiring a redundant top-level
	// summary for batches. The handler enforces this too: SDK validation only
	// supports a subset of JSON Schema.
	schema["anyOf"] = []any{
		map[string]any{"required": []string{"summary"}},
		map[string]any{"required": []string{"facts"}},
	}
	return schema
}

// RepairBuiltinToolSchema restores code-owned contracts on old catalog rows.
// Descriptions, defaults and extra properties remain editable. It also runs at
// resolution time so an old cached/editor schema cannot reintroduce the defect.
func RepairBuiltinToolSchema(name string, stored map[string]any) map[string]any {
	var canonical map[string]any
	switch name {
	case "record_fact":
		canonical = recordFactSchema()
	case "list_task_credentials":
		canonical = obj(map[string]any{})
	default:
		return stored
	}
	raw, _ := json.Marshal(stored)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	if out == nil {
		out = map[string]any{}
	}
	mergeToolContract(out, canonical)
	// Neither tool unconditionally requires a top-level field: record_fact has
	// alternative single/batch shapes; list_task_credentials takes no arguments.
	delete(out, "required")
	return out
}

func mergeToolContract(dst, src map[string]any) {
	for key, value := range src {
		if key == "description" || key == "default" {
			if _, exists := dst[key]; exists {
				continue
			}
		}
		if child, ok := value.(map[string]any); ok {
			target, _ := dst[key].(map[string]any)
			if target == nil {
				target = map[string]any{}
				dst[key] = target
			}
			mergeToolContract(target, child)
		} else {
			dst[key] = value
		}
	}
}
