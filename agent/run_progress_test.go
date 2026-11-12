package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/permission"
	actool "github.com/Autumn-27/norma/tool"
)

// Reproduce round 234's six alternating, byte-identical graph queries without
// contacting targets or loading any private response data.
func TestRunProgressAlternatingReadsAndBoundedSettlement(t *testing.T) {
	parent := t.Context()
	ctx, g := newRunProgress(parent, nil, 0)
	defer g.Close()
	for i := 0; i < 6*(unchangedResultLimit-1)+1; i++ {
		input := []byte(fmt.Sprintf(`{"q":"query-%d"}`, i%6))
		if block, _, _ := g.PreToolUse(ctx, "list_facts", input); block {
			t.Fatal("stopped before threshold")
		}
		g.PostToolUse(ctx, "list_facts", input, []byte(`"unchanged"`), false)
	}
	if !errors.Is(g.Err(), errNoProgress) {
		t.Fatal("alternating query loop not detected")
	}
	if block, msg, _ := g.PreToolUse(ctx, "Bash", []byte(`{}`)); !block || !strings.Contains(msg, "checkpoint") {
		t.Fatal("exploration continued")
	}
	if block, _, _ := g.PreToolUse(ctx, "record_fact", []byte(`{"summary":"checkpoint"}`)); block {
		t.Fatal("cannot save progress")
	}
	for i := 0; i < settlementToolLimit; i++ {
		g.PreToolUse(ctx, "list_facts", []byte(`{}`))
	}
	if ctx.Err() == nil || parent.Err() != nil {
		t.Fatal("limit failed or cancelled parent task")
	}
}

func TestRunProgressAllowsChangedResultsPaginationAndExternalPolling(t *testing.T) {
	ctx, g := newRunProgress(t.Context(), nil, 0)
	defer g.Close()
	for i := 0; i < 20; i++ {
		for _, call := range []struct{ name, input, result string }{
			{"list_facts", `{"q":"same"}`, fmt.Sprint(i)},
			{"list_facts", fmt.Sprintf(`{"before":%d}`, i), "same-page-body"},
			{"TaskOutput", `{"id":"job"}`, "running"},
		} {
			if block, _, _ := g.PreToolUse(ctx, call.name, []byte(call.input)); block {
				t.Fatal("legitimate progress blocked")
			}
			g.PostToolUse(ctx, call.name, []byte(call.input), []byte(call.result), false)
		}
	}
	if g.Err() != nil {
		t.Fatal(g.Err())
	}
	if progressKey("list_facts", []byte(`{"q":"x","before":2}`)) != progressKey("list_facts", []byte(`{ "before": 2, "q": "x" }`)) {
		t.Fatal("object key order defeats detection")
	}
	if progressKey("node_detail", []byte(`{"id":9007199254740992}`)) == progressKey("node_detail", []byte(`{"id":9007199254740993}`)) {
		t.Fatal("large IDs rounded")
	}
}

// Exercise the actual SDK hook convention (true means block), not just a local
// detector. A noncompliant model keeps requesting the same tool until cancelled.
func TestRunProgressSDKLoop(t *testing.T) {
	parent, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	ctx, g := newRunProgress(parent, nil, 0)
	defer g.Close()
	turn, executed := 0, 0
	provider := captureUsageProvider{stream: func(ctx context.Context, yield func(llm.StreamEvent, error) bool) {
		if ctx.Err() != nil {
			yield(llm.StreamEvent{}, ctx.Err())
			return
		}
		turn++
		for _, ev := range []llm.StreamEvent{
			{Type: llm.SEToolUseStart, ToolID: fmt.Sprint(turn), ToolName: "list_facts"},
			{Type: llm.SEToolInputJSON, Text: `{"q":"same"}`},
			{Type: llm.SEMessageDelta, StopReason: "tool_use"},
		} {
			if !yield(ev, nil) {
				return
			}
		}
	}}
	tool := actool.Build(actool.Spec{Name: "list_facts", Schema: obj(map[string]any{"q": str("query")}), Run: func(context.Context, json.RawMessage, *actool.ToolContext) (actool.Result, error) {
		executed++
		return actool.Text("unchanged"), nil
	}})
	var terminal db.Activity
	_, _, _ = captureRun(ctx, agentcore.Options{Provider: provider, Tools: []actool.CoreTool{tool}, Hooks: g, PermissionMode: permission.ModeBypass}, "review stored facts", func(a db.Activity) {
		if a.Kind == "result" {
			terminal = a
		}
	})
	if !terminal.IsError || !strings.Contains(terminal.Detail, errNoProgress.Error()) {
		t.Fatal("audit lost no-progress reason", terminal)
	}
	if executed != unchangedResultLimit || turn > unchangedResultLimit+settlementToolLimit+2 || !errors.Is(g.Err(), errRunLimit) {
		t.Fatalf("executed=%d turns=%d err=%v", executed, turn, g.Err())
	}
}

func TestRunProgressTimeBudgetAndParentCancel(t *testing.T) {
	parent, cancel := context.WithCancel(t.Context())
	ctx, g := newRunProgress(parent, nil, time.Millisecond)
	defer g.Close()
	// Synchronize on the budget transition rather than assuming timer scheduling.
	deadline := time.After(time.Second)
	for {
		g.mu.Lock()
		settling := g.settling
		g.mu.Unlock()
		if settling {
			break
		}
		select {
		case <-deadline:
			t.Fatal("budget did not trigger")
		case <-time.After(time.Millisecond):
		}
	}
	if block, _, _ := g.PreToolUse(ctx, "Bash", nil); !block {
		t.Fatal("time budget did not restrict exploration")
	}
	if block, _, _ := g.PreToolUse(ctx, "report_finding", nil); block {
		t.Fatal("cannot persist known finding")
	}
	cancel()
	if ctx.Err() == nil {
		t.Fatal("pause did not propagate")
	}
}
