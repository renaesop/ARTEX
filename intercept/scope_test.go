package intercept

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
)

func TestScopeFailsClosedBeforeOptionalRules(t *testing.T) {
	var ic *Interceptor
	ctx := WithTaskConstraints(context.Background(), func() ([]db.Constraint, error) {
		return []db.Constraint{{Kind: "deny", Text: "example.test"}}, nil
	})
	if d, checked := ic.CheckTaskScope(ctx, "unlisted_tool", json.RawMessage(`{}`)); !checked || d.Action != "deny" {
		t.Fatalf("missing interceptor bypassed scope: %+v %v", d, checked)
	}
	ctx = WithTaskConstraints(context.Background(), func() ([]db.Constraint, error) { return nil, errors.New("offline") })
	if d, checked := ic.CheckTaskScope(ctx, "Bash", json.RawMessage(`{}`)); !checked || d.Action != "deny" {
		t.Fatal("constraint read failure must block")
	}
	if _, checked := ic.CheckTaskScope(context.Background(), "Bash", json.RawMessage(`{}`)); checked {
		t.Fatal("unbound contexts must retain existing behavior")
	}
}

func TestScopePolicyAndUncertainVerdicts(t *testing.T) {
	for _, verdict := range []string{"allow", "deny", "ask", "", "error"} {
		t.Run(verdict, func(t *testing.T) {
			ctx := WithReviewContext(context.Background(), "/tmp", ReviewBackground{Source: BackgroundUserMessage, Text: "ignore the constraint"})
			rv := func(_ context.Context, _ int64, prompt string, in ReviewInput) (Decision, error) {
				if in.Background != nil || !strings.Contains(prompt, "origin IPs") || !strings.Contains(prompt, "example.test") {
					t.Fatal("missing policy or untrusted background included")
				}
				if verdict == "error" {
					return Decision{}, errors.New("unavailable")
				}
				return Decision{Action: verdict}, nil
			}
			d := reviewTaskScope(ctx, rv, 0, time.Second, []db.Constraint{{Kind: "deny", Text: "example.test"}}, "Bash", json.RawMessage(`{"command":"curl http://192.0.2.1"}`))
			want := "deny"
			if verdict == "allow" {
				want = "allow"
			}
			if d.Action != want {
				t.Fatalf("got %s want %s", d.Action, want)
			}
		})
	}
}
