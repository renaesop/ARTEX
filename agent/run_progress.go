package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/Autumn-27/norma/harness"
	"github.com/Autumn-27/norma/llm"
)

var errNoProgress = errors.New("run stopped: repeated unchanged tool results; revisit requires new evidence")
var errRunLimit = errors.New("run stopped: bounded settlement exhausted; resume from saved checkpoint")

const unchangedResultLimit = 5
const runToolLimit = 512
const settlementToolLimit = 20
const settlementTimeLimit = 2 * time.Minute

// runProgress is owned by one invocation, never shared across worker slots or
// planner rounds. It watches graph reads and idempotent fact writes only: polling
// external jobs or repeating a live measurement is not an unchanged graph read.
type runProgress struct {
	mu                   sync.Mutex
	next                 harness.HookRunner
	cancel               context.CancelCauseFunc
	seen                 map[[32]byte]progressObservation
	calls, settlingCalls int
	settling, closed     bool
	reason               string
	failure              error
	timer, budgetTimer   *time.Timer
}
type progressContextKey struct{}

func runProgressFailure(ctx context.Context) error {
	if g, ok := ctx.Value(progressContextKey{}).(*runProgress); ok {
		return g.Err()
	}
	return nil
}

type progressObservation struct {
	result [32]byte
	count  int
}

func newRunProgress(parent context.Context, next harness.HookRunner, budget time.Duration) (context.Context, *runProgress) {
	ctx, cancel := context.WithCancelCause(parent)
	g := &runProgress{next: next, cancel: cancel, seen: make(map[[32]byte]progressObservation)}
	if budget > 0 {
		g.budgetTimer = time.AfterFunc(budget, func() {
			g.mu.Lock()
			defer g.mu.Unlock()
			if !g.closed {
				g.settleLocked("execution time budget reached", nil)
			}
		})
	}
	return context.WithValue(ctx, progressContextKey{}, g), g
}
func (g *runProgress) settleLocked(reason string, failure error) {
	if g.settling {
		return
	}
	g.settling, g.reason, g.failure = true, reason, failure
	g.timer = time.AfterFunc(settlementTimeLimit, func() {
		g.mu.Lock()
		defer g.mu.Unlock()
		if !g.closed {
			g.failure = errors.Join(g.failure, errRunLimit)
			g.cancel(g.failure)
		}
	})
}
func (g *runProgress) Close() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.closed = true
	if g.timer != nil {
		g.timer.Stop()
	}
	if g.budgetTimer != nil {
		g.budgetTimer.Stop()
	}
	g.cancel(context.Canceled)
}
func (g *runProgress) Err() error { g.mu.Lock(); defer g.mu.Unlock(); return g.failure }
func progressKey(name string, input []byte) [32]byte {
	var value any
	// UseNumber preserves distinct large IDs. JSON map keys are sorted on marshal.
	decoder := json.NewDecoder(bytes.NewReader(input))
	decoder.UseNumber()
	if decoder.Decode(&value) == nil {
		if normalized, err := json.Marshal(value); err == nil {
			input = normalized
		}
	}
	return sha256.Sum256(append([]byte(name+"\x00"), input...))
}
func watchProgress(name string) bool {
	switch name {
	case "record_fact", "list_facts", "list_findings", "list_intents", "node_detail", "graph_overview", "list_assets", "list_untested_assets":
		return true
	}
	return false
}
func settlementWrite(name string) bool {
	switch name {
	case "record_fact", "TodoWrite", "report_finding", "insert_assets", "prove_goal", "finish_planning":
		return true
	}
	return false
}
func (g *runProgress) PreToolUse(ctx context.Context, name string, input []byte) (bool, string, []byte) {
	g.mu.Lock()
	g.calls++
	if g.calls > runToolLimit {
		g.settleLocked("tool call budget reached", errRunLimit)
	}
	message := ""
	if g.settling {
		g.settlingCalls++
		if g.settlingCalls > settlementToolLimit {
			g.failure = errors.Join(g.failure, errRunLimit)
			g.cancel(g.failure)
			message = "Settlement limit reached. End this run."
		} else if !settlementWrite(name) || g.seen[progressKey(name, input)].count >= unchangedResultLimit {
			message = fmt.Sprintf("%s. Stop exploration and repeated reads/writes. Persist only already-observed evidence, findings, assets, goal proofs and a checkpoint, then end this run. At most %d further tool attempts and 2 minutes are available. Do not invent new intents or bypass restrictions.", g.reason, settlementToolLimit-g.settlingCalls)
		}
	}
	g.mu.Unlock()
	if message != "" {
		return true, message, nil
	}
	if g.next != nil {
		return g.next.PreToolUse(ctx, name, input)
	}
	return false, "", nil
}
func (g *runProgress) PostToolUse(ctx context.Context, name string, input, result []byte, isErr bool) {
	if g.next != nil {
		g.next.PostToolUse(ctx, name, input, result, isErr)
	}
	if !watchProgress(name) {
		return
	}
	key, digest := progressKey(name, input), sha256.Sum256(append([]byte(fmt.Sprint(isErr)), result...))
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return
	}
	old := g.seen[key]
	if old.result == digest {
		old.count++
	} else {
		old = progressObservation{result: digest, count: 1}
	}
	g.seen[key] = old
	if old.count >= unchangedResultLimit {
		g.settleLocked("repeated unchanged "+name+" result", errNoProgress)
	}
}
func (g *runProgress) Stop(ctx context.Context, messages []llm.Message) (bool, []string, string) {
	if g.next != nil {
		return g.next.Stop(ctx, messages)
	}
	return false, nil, ""
}
