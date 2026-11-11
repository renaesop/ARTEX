package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/tool"
	"golang.org/x/net/html"
)

//go:embed browse_render.cjs
var browseRenderer string

type BrowseExit struct {
	Name  string
	Proxy *url.URL
}

var configuredExits = sync.OnceValues(func() ([]BrowseExit, error) {
	return parseBrowseExits(os.Getenv("ARTEX_BROWSE_EXITS"))
})

func ValidateBrowseExits() error { _, err := configuredExits(); return err }

func parseBrowseExits(raw string) ([]BrowseExit, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	seen := map[string]bool{}
	var exits []BrowseExit
	for _, entry := range strings.Split(raw, ",") {
		name, addr, ok := strings.Cut(strings.TrimSpace(entry), "=")
		u, err := url.Parse(addr)
		if !ok || name == "" || seen[name] || err != nil || u.Scheme != "http" || !net.ParseIP(u.Hostname()).IsLoopback() || u.Port() == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
			return nil, errors.New("ARTEX_BROWSE_EXITS requires distinct name=http://loopback:port entries")
		}
		seen[name] = true
		exits = append(exits, BrowseExit{name, u})
	}
	return exits, nil
}

func SessionBrowseExit(ctx context.Context, role string) *BrowseExit {
	exits, _ := configuredExits() // validated before the manager starts
	return selectBrowseExit(exits, RunInfoFrom(ctx), role)
}

// Rendezvous hashing keeps a session pinned across requests, concurrent agents,
// process restarts and changes to the ordering of the same configured exits.
func selectBrowseExit(exits []BrowseExit, run RunInfo, role string) *BrowseExit {
	var key string
	switch {
	case run.SessionID != "":
		key = "chat/" + run.SessionID
	case run.ExplorationID != 0:
		key = fmt.Sprintf("exp/%d/%s/%d", run.ExplorationID, role, run.IntentID)
	default:
		return nil
	}
	var best *BrowseExit
	var score [32]byte
	for i := range exits {
		s := sha256.Sum256([]byte(key + "\x00" + exits[i].Name))
		if best == nil || bytes.Compare(s[:], score[:]) > 0 {
			best = &exits[i]
			score = s
		}
	}
	return best
}

type NetworkToolCall func(context.Context, json.RawMessage, *tool.ToolContext) (tool.Result, error)

type purposeTool struct {
	tool.CoreTool
	browse NetworkToolCall
}

// PurposeTool requires the caller to select its network path before executing the wrapped tool.
func PurposeTool(original tool.CoreTool, browse NetworkToolCall) tool.CoreTool {
	return &purposeTool{original, browse}
}
func (t *purposeTool) Description() string {
	return t.CoreTool.Description() + " Set purpose=browse for ordinary reading or navigation, and purpose=security for reconnaissance, probes and testing. Keep the same purpose for follow-up browser actions. Never retry a denial through another route."
}
func (t *purposeTool) InputSchema() map[string]any {
	s := maps.Clone(t.CoreTool.InputSchema())
	if s == nil {
		s = map[string]any{"type": "object"}
	}
	props, _ := s["properties"].(map[string]any)
	props = maps.Clone(props)
	if props == nil {
		props = map[string]any{}
	}
	props["purpose"] = map[string]any{"type": "string", "enum": []string{"browse", "security"}}
	if t.Name() == "WebFetch" && filepath.IsAbs(os.Getenv("ARTEX_BROWSE_PLAYWRIGHT")) {
		props["render"] = map[string]any{"type": "boolean", "description": "For purpose=browse, render JavaScript in a fresh browser on the same pinned exit."}
	}
	s["properties"] = props
	var req []string
	switch v := s["required"].(type) {
	case []string:
		req = append(req, v...)
	case []any:
		for _, r := range v {
			if k, ok := r.(string); ok {
				req = append(req, k)
			}
		}
	}
	s["required"] = append(req, "purpose")
	return s
}
func (t *purposeTool) Call(ctx context.Context, in json.RawMessage, tc *tool.ToolContext) (tool.Result, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(in, &fields); err != nil {
		return tool.Result{}, err
	}
	var purpose string
	_ = json.Unmarshal(fields["purpose"], &purpose)
	delete(fields, "purpose")
	clean, err := json.Marshal(fields)
	if err != nil {
		return tool.Result{}, err
	}
	switch purpose {
	case "security":
		if t.Name() == "WebFetch" && string(fields["render"]) == "true" {
			return tool.Errorf("Rendered WebFetch requires purpose=browse; use the testing browser for a security test."), nil
		}
		return t.CoreTool.Call(ctx, clean, tc)
	case "browse":
		return t.browse(ctx, clean, tc)
	default:
		return tool.Errorf("Select purpose=browse for ordinary browsing or purpose=security for a security test."), nil
	}
}

func configureSessionBrowsing(ctx context.Context, role string, opts *agentcore.Options) {
	exit := SessionBrowseExit(ctx, role)
	if exit == nil {
		return
	}
	opts.BashEnv = append(opts.BashEnv, "ARTEX_BROWSE_PROXY="+exit.Proxy.String())
	opts.SystemPrompt = append(opts.SystemPrompt, "For ordinary browsing use WebFetch or the browser MCP with purpose=browse. For reconnaissance, security tests and payloads use purpose=security and the existing testing route. For an ordinary download in Bash use curl --noproxy '' --proxy \"$ARTEX_BROWSE_PROXY\"; other Bash tools keep the testing route. On 403, 429 or a challenge do not switch routes to retry.")
	if opts.EnableWebFetch {
		security := tool.NewWebFetch(tool.WebFetchConfig{Proxy: opts.WebFetchProxy, CACert: opts.WebFetchCACert, InsecureTLS: opts.WebFetchInsecureTLS})
		opts.Tools = append(opts.Tools, PurposeTool(security, exit.fetch))
		opts.EnableWebFetch = false // avoid also registering the SDK's unclassified WebFetch
	}
}

func (e BrowseExit) fetch(ctx context.Context, raw json.RawMessage, _ *tool.ToolContext) (tool.Result, error) {
	var in struct {
		URL     string `json:"url"`
		Extract bool   `json:"extract"`
		Render  bool   `json:"render"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return tool.Result{}, err
	}
	if in.Extract {
		return tool.Errorf("Recon extraction requires purpose=security."), nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, in.URL, nil)
	if err != nil || (req.URL.Scheme != "http" && req.URL.Scheme != "https") || req.URL.User != nil || req.URL.Hostname() == "" {
		return tool.Errorf("Provide an HTTP(S) URL without embedded credentials."), nil
	}
	if in.Render {
		return e.render(ctx, in.URL)
	}
	// A fixed Proxy callback also applies to redirects. Unlike the SDK recording
	// proxy path, this client never retries directly when the proxy fails.
	tr := &http.Transport{Proxy: http.ProxyURL(e.Proxy), TLSHandshakeTimeout: 10 * time.Second}
	defer tr.CloseIdleConnections()
	client := http.Client{Transport: tr, Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return tool.Errorf(fmt.Sprintf("Browsing exit %s failed: %v", e.Name, err)), nil
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return tool.Errorf(fmt.Sprintf("HTTP %d through %s; keep the same exit.", resp.StatusCode, e.Name)), nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return tool.Errorf(err.Error()), nil
	}
	ct := strings.ToLower(resp.Header.Get("Content-Type"))
	if strings.Contains(ct, "text/html") {
		z := html.NewTokenizer(bytes.NewReader(body))
		var out strings.Builder
		hidden := false
		var href string
		for tok := z.Next(); tok != html.ErrorToken; tok = z.Next() {
			switch tok {
			case html.StartTagToken, html.EndTagToken:
				name, _ := z.TagName()
				switch string(name) {
				case "a":
					if tok == html.StartTagToken {
						href = ""
						for more := true; more; {
							key, val, next := z.TagAttr()
							more = next
							if string(key) == "href" {
								link, err := resp.Request.URL.Parse(string(val))
								if err == nil && (link.Scheme == "http" || link.Scheme == "https") {
									href = link.String()
								}
							}
						}
					} else if !hidden && href != "" {
						fmt.Fprintf(&out, " (%s)\n", href)
						href = ""
					}
				case "script", "style", "head", "noscript", "template":
					hidden = tok == html.StartTagToken
				}
			case html.TextToken:
				if !hidden {
					if s := strings.TrimSpace(string(z.Text())); s != "" {
						out.WriteString(s)
						out.WriteByte('\n')
					}
				}
			}
		}
		body = []byte(out.String())
	} else if ct != "" && !strings.HasPrefix(ct, "text/") && !strings.Contains(ct, "json") && !strings.Contains(ct, "xml") {
		return tool.Errorf("Use WebFetch for documents and text responses."), nil
	}
	text := []rune(string(body))
	if len(text) > 30000 {
		text = append(text[:30000], []rune("\n[truncated]")...)
	}
	return tool.Text(fmt.Sprintf("HTTP %d via %s\n%s", resp.StatusCode, e.Name, string(text))), nil
}

func (e BrowseExit) render(ctx context.Context, target string) (tool.Result, error) {
	if !filepath.IsAbs(os.Getenv("ARTEX_BROWSE_PLAYWRIGHT")) {
		return tool.Errorf("No managed browsing renderer is configured."), nil
	}
	ctx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", "-e", browseRenderer, "--", e.Proxy.String(), target)
	cmd.Dir = "/"
	// Browser children get only their runtime paths, not the service's provider or storage credentials.
	for _, key := range []string{"PATH", "HOME", "TMPDIR", "LANG", "PLAYWRIGHT_BROWSERS_PATH", "ARTEX_BROWSE_PLAYWRIGHT", "ARTEX_BROWSE_CHROMIUM"} {
		if v, ok := os.LookupEnv(key); ok {
			cmd.Env = append(cmd.Env, key+"="+v)
		}
	}
	cmd.WaitDelay = 3 * time.Second
	out, err := cmd.Output()
	if err != nil {
		return tool.Errorf(fmt.Sprintf("Browser on exit %s could not load the page: %v", e.Name, err)), nil
	}
	var page struct {
		Status int
		Body   string
	}
	if err := json.Unmarshal(out, &page); err != nil {
		return tool.Result{}, err
	}
	if page.Status >= 400 {
		return tool.Errorf(fmt.Sprintf("HTTP %d through %s; keep the same exit.", page.Status, e.Name)), nil
	}
	return tool.Text(fmt.Sprintf("HTTP %d via %s\n%s", page.Status, e.Name, page.Body)), nil
}
