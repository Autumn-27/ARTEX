package agent

import (
	"context"

	actool "github.com/Autumn-27/norma/tool"
)

// DeferredInfo carries the deferred-tools wiring an agent needs to build its
// Options: the MCP tool names whose schemas are withheld, the subset listed in the
// global system-prompt block (non-skill-gated), and the shared session unlock set.
// UnlockSkill unlocks a named skill's MCPs — hosts call it to rebuild the unlock set
// from history on a resumed session (design doc C2).
type DeferredInfo struct {
	Deferred    []string          // all MCP tool names (schema withheld)
	GlobalNames []string          // MCP names to list in the system-prompt block
	Unlock      *actool.UnlockSet // shared call-gate; nil when no MCP tools
	UnlockSkill func(skillName string)
}

// ToolAugment, if set, returns the EXTRA tools an agent should see beyond its
// built-in base set — the agent's visible skills (packed into one Skill meta-tool)
// and visible MCP servers (expanded to mcp__server__tool). It also returns the
// DeferredInfo describing how those MCP tools are deferred/gated. The server wires
// it to the PG agent_visibility table. cleanup releases any spawned MCP clients.
//
// When nil, agents run with only their built-in tools — behavior is unchanged
// until a user assigns a skill/MCP to the agent in the UI.
var ToolAugment func(ctx context.Context, agentKey string) (extra []actool.CoreTool, def DeferredInfo, cleanup func())

// AugmentTools returns base plus the agent's visible skill/MCP tools, the
// DeferredInfo, and a cleanup func the caller must defer (closes MCP clients).
// Built-in base tools are kept as-is — never filtered (内置工具留代码层，不做可见性过滤).
func AugmentTools(ctx context.Context, agentKey string, base []actool.CoreTool) ([]actool.CoreTool, DeferredInfo, func()) {
	var (
		def     DeferredInfo
		cleanup = func() {}
		out     = base
	)
	if ToolAugment != nil {
		var extra []actool.CoreTool
		var cl func()
		extra, def, cl = ToolAugment(ctx, agentKey)
		if cl != nil {
			cleanup = cl
		}
		if len(extra) > 0 {
			out = append(append([]actool.CoreTool{}, base...), extra...)
		}
	}
	// DB tools table has the final say on the built-in tools: drop the ones this
	// agent isn't bound to (or that are disabled) and swap in overridden
	// descriptions/schemas + default injection. MCP/skill/host tools have no row
	// and pass through untouched, so deferred/unlock wiring stays consistent.
	if ToolResolve != nil {
		out = ToolResolve(ctx, agentKey, out)
	}
	return out, def, cleanup
}
