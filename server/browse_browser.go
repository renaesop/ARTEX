package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/norma/tool"
)

// Created once per tool assembly. A browsing call must never reuse a browser
// launched for security testing, as Chromium's proxy is selected at launch.
type browsingBrowser struct {
	mu     sync.Mutex
	config db.MCPServer
	exit   *agent.BrowseExit
	client mcpClient
	tools  map[string]tool.CoreTool
}

func (b *browsingBrowser) call(name string) agent.NetworkToolCall {
	return func(ctx context.Context, in json.RawMessage, tc *tool.ToolContext) (tool.Result, error) {
		b.mu.Lock()
		defer b.mu.Unlock()
		switch strings.TrimPrefix(name, "mcp__browser__") {
		case "browser_navigate", "browser_navigate_back", "browser_snapshot", "browser_click", "browser_hover", "browser_press_key", "browser_tabs", "browser_wait_for", "browser_take_screenshot", "browser_type", "browser_fill_form", "browser_select_option", "browser_close":
		default:
			return tool.Errorf("This browser operation requires purpose=security."), nil
		}
		if b.client == nil {
			args := stripProxyArgs(decodeStrSlice(b.config.Args))
			isolated := false
			for _, a := range args {
				flag, _, _ := strings.Cut(a, "=")
				switch flag {
				case "--isolated":
					isolated = true
				case "--config", "--cdp-endpoint", "--endpoint", "--extension", "--user-data-dir", "--shared-browser-context", "--caps":
					return tool.Errorf("This custom browser configuration cannot provide an isolated browsing route."), nil
				}
			}
			if !isolated {
				args = append(args, "--isolated")
			}
			b.config.Args = encodeJSON(append(args, "--proxy-server", b.exit.Proxy.String(), "--proxy-bypass", "<-loopback>"))
			c, err := connectMCP(ctx, &b.config)
			if err != nil {
				return tool.Errorf(fmt.Sprintf("Start browsing browser on %s: %v", b.exit.Name, err)), nil
			}
			ts, err := c.Tools(ctx)
			if err != nil {
				_ = c.Close()
				return tool.Result{}, err
			}
			b.tools = make(map[string]tool.CoreTool, len(ts))
			for _, t := range ts {
				b.tools[t.Name()] = t
			}
			b.client = c
		}
		t := b.tools[name]
		if t == nil {
			return tool.Errorf("Tool unavailable in isolated browsing browser."), nil
		}
		return t.Call(ctx, in, tc)
	}
}
func (b *browsingBrowser) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.client != nil {
		return b.client.Close()
	}
	return nil
}
