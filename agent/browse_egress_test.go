package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/tool"
)

func TestBrowseExitSessionPin(t *testing.T) {
	exits, err := parseBrowseExits("vm9=http://127.0.0.1:19209,vm5=http://127.0.0.1:19205")
	if err != nil {
		t.Fatal(err)
	}
	reversed := slices.Clone(exits)
	slices.Reverse(reversed)
	used := map[string]bool{}
	for i := 0; i < 40; i++ {
		run := RunInfo{ExplorationID: 43, IntentID: int64(i + 1)}
		picked := selectBrowseExit(exits, run, "worker")
		if other := selectBrowseExit(reversed, run, "worker"); other.Name != picked.Name {
			t.Fatal("binding depends on list order")
		}
		used[picked.Name] = true
	}
	if len(used) != 2 {
		t.Fatal("only one exit used")
	}
	for _, bad := range []string{"x=http://example.org:80", "x=http://localhost:80", "x=:", "x=http://127.0.0.1:1,x=http://127.0.0.1:2"} {
		if _, err := parseBrowseExits(bad); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
}
func TestBrowseIsolation(t *testing.T) {
	var direct, proxied atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		direct.Add(1)
		w.Header().Set("Content-Type", "text/plain")
		io.WriteString(w, "testing path")
	}))
	defer target.Close()
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxied.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	u, _ := url.Parse(proxy.URL)
	route := BrowseExit{"exit-test", u}
	wf := PurposeTool(tool.NewWebFetch(tool.WebFetchConfig{}), route.fetch)
	invoke := func(purpose string) tool.Result {
		raw, _ := json.Marshal(map[string]any{"purpose": purpose, "url": target.URL})
		got, err := wf.Call(t.Context(), raw, nil)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	if !invoke("browse").IsError || proxied.Load() != 1 || direct.Load() != 0 {
		t.Fatal("429 bypassed the pinned exit")
	}
	proxy.Close()
	if !invoke("browse").IsError || direct.Load() != 0 {
		t.Fatal("dead proxy fell back to target")
	}
	if !invoke("").IsError || direct.Load() != 0 {
		t.Fatal("missing purpose used network")
	}
	if invoke("security").IsError || direct.Load() != 1 {
		t.Fatal("security route did not use the separate transport")
	}
}

// Opt-in deployment check. Uses only the supplied echo endpoint and contains no credentials.
func TestBrowseLivePaths(t *testing.T) {
	endpoint := os.Getenv("ARTEX_TEST_BROWSE_ECHO")
	if endpoint == "" {
		t.Skip("live egress check disabled")
	}
	expected := map[string]string{}
	for _, kv := range strings.Split(os.Getenv("ARTEX_TEST_EXIT_IPS"), ",") {
		k, v, _ := strings.Cut(kv, "=")
		expected[k] = v
	}
	seen := map[string]bool{}
	for i := 0; i < 128 && len(seen) < len(expected); i++ {
		ctx := WithRunInfo(context.Background(), RunInfo{SessionID: fmt.Sprintf("test-browse-%d", i)})
		exit := SessionBrowseExit(ctx, "chat")
		if exit == nil {
			t.Fatal("missing exits")
		}
		want, ok := expected[exit.Name]
		if !ok {
			t.Fatal("unknown exit", exit.Name)
		}
		if seen[exit.Name] {
			continue
		}
		seen[exit.Name] = true
		opts := agentcore.Options{EnableWebFetch: true}
		configureSessionBrowsing(ctx, "chat", &opts)
		if opts.EnableWebFetch || len(opts.Tools) != 1 {
			t.Fatal("missing routed WebFetch")
		}
		for _, check := range []struct {
			purpose string
			render  bool
			ip      string
		}{{"browse", false, want}, {"browse", true, want}, {"browse", false, want}, {"security", false, os.Getenv("ARTEX_TEST_DIRECT_IP")}} {
			raw, _ := json.Marshal(map[string]any{"url": endpoint, "purpose": check.purpose, "render": check.render})
			got, err := opts.Tools[0].Call(ctx, raw, nil)
			if err != nil || got.IsError || len(got.Content) == 0 || !strings.Contains(got.Content[0].Text, check.ip) {
				t.Fatalf("exit=%s purpose=%s render=%t: %v %+v", exit.Name, check.purpose, check.render, err, got.Content)
			}
			t.Logf("%s %s render=%t ip=%s", exit.Name, check.purpose, check.render, check.ip)
		}
	}
	if len(seen) != len(expected) {
		t.Fatal("not all exits observed")
	}
	client, err := quotaAwareHTTPClient("", "")
	if err != nil {
		t.Fatal(err)
	}
	r, err := client.Get(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	body, err := io.ReadAll(r.Body)
	if err != nil || strings.TrimSpace(string(body)) != os.Getenv("ARTEX_TEST_DIRECT_IP") {
		t.Fatalf("model transport: %s %v", body, err)
	}
	t.Logf("model transport: %s", body)
}
