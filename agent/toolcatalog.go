package agent

import (
	"context"
	"encoding/json"

	actool "github.com/Autumn-27/norma/tool"
)

// 本文件把「内置工具」从纯代码变成可枚举、可被 DB 覆盖的目录：
//   - BuiltinToolSeeds()：把三个执行 agent 的内置工具集展开成 seed 记录（key +
//     描述 + 参数 schema + 默认绑定的 agent），供服务端开机幂等播种进 tools 表。
//   - ToolResolve 钩子：运行时按 DB 里的 tools 行对已装配的工具做「按 agent 过滤 +
//     覆盖描述/schema + 注入参数默认值」。key/handler 仍在代码层，DB 只改「散文与默认值」。
// handler（Call 行为）永远来自代码——DB 改不了它，只能改模型看到的说明与缺省入参。

// ToolSeed 是一个内置工具的可播种快照：key 即 CoreTool.Name()（与 handler 死绑，
// UI 只读），Desc/Schema 取自代码里的工具定义，Agents 是代码默认把它给了哪些 agent。
type ToolSeed struct {
	Key    string         // = CoreTool.Name()，主键，不可改
	Desc   string         // 顶层描述（可在 UI 覆盖）
	Schema map[string]any // 参数 JSON-Schema（结构只读，description/default 可在 UI 改）
	Agents []string       // 默认绑定的 agent key（worker/planner/mainagent）
}

// builtinToolsByAgent 用一个「只读空壳」ToolSet（nil stores）构造每个执行 agent 的
// 领域工具集。工具构造函数只把闭包塞进 Spec、构造期不解引用 store，所以 nil 安全——
// 这些工具在这里只用来读 Name()/Description()/InputSchema()，绝不 Call。
//
// 刻意【不含】SDK 通用工具 actool.DefaultTools()（Read/Write/Edit/MultiEdit/LS/Glob/
// Grep/Bash）：它们每个 agent 都固定拥有、没有「绑定到谁」的取舍，且说明大多在 Prompt()
// 里（本表只覆盖 Description()，会造成半覆盖误导）。不 seed → 无 DB 行 → ToolResolve
// 原样放行、不覆盖，行为与从前一致。只有 artex 自己的领域工具入表可管。
func builtinToolsByAgent() map[string][]actool.CoreTool {
	ts := NewToolSet(nil, "")
	return map[string][]actool.CoreTool{
		"mainagent": ts.MainAgentTools(),
		"planner":   ts.PlannerTools(),
		"worker":    ts.WorkerTools(),
		// auto 默认绑漏洞上报 + 资产管理工具，其他域工具可在 UI 按需勾选。
		// 新库由此 seed 写入；老库由 seedAutoDefaultBindings 迁移。
		"auto": {ts.addFinding(), ts.insertAssets(), ts.addCompanyScope(), ts.listAssets(), ts.listCompanies()},
		// pentest（独立渗透 agent）默认绑：查资产 / 插资产 / 报漏洞 / 查漏洞 / 查企业。
		// 新库由此 seed 写入；老库由 seedPentestDefaultBindings 迁移。
		"pentest": {ts.listAssets(), ts.insertAssets(), ts.addFinding(), ts.listFindings(), ts.listCompanies()},
	}
}

// BuiltinToolSeeds 把三个 agent 的内置工具集去重合并成 seed 列表：同名工具（如
// list_assets 三者都有）合成一条，Agents 取并集。顺序稳定（mainagent→planner→worker）。
func BuiltinToolSeeds() []ToolSeed {
	byAgent := builtinToolsByAgent()
	order := []string{"mainagent", "planner", "worker", "auto", "pentest"}

	type acc struct {
		tool   actool.CoreTool
		agents []string
	}
	m := map[string]*acc{}
	var keys []string
	for _, ak := range order {
		for _, t := range byAgent[ak] {
			a, ok := m[t.Name()]
			if !ok {
				a = &acc{tool: t}
				m[t.Name()] = a
				keys = append(keys, t.Name())
			}
			a.agents = append(a.agents, ak)
		}
	}

	out := make([]ToolSeed, 0, len(keys))
	for _, k := range keys {
		a := m[k]
		out = append(out, ToolSeed{
			Key:    k,
			Desc:   a.tool.Description(),
			Schema: a.tool.InputSchema(),
			Agents: a.agents,
		})
	}
	return out
}

// ToolResolve, if set, post-processes an agent's fully-assembled tool list against
// the DB tools table: it drops tools not bound to this agent (or globally disabled)
// and wraps the rest so the model sees the DB-overridden description/schema and
// 缺省入参 get injected. Tools with no matching DB row (MCP/skill/host tools like
// traffic) pass through untouched. nil = tools unchanged. Wired in server/assembly.go.
var ToolResolve func(ctx context.Context, agentKey string, tools []actool.CoreTool) []actool.CoreTool

// DecorateTool wraps t so Description()/InputSchema() report the DB overrides and
// Call() injects scalar parameter defaults (from schema's "default" props) whenever
// the model omitted them. Name/Prompt/permission/scheduler flags delegate to t, so
// the tool's identity and handler are unchanged. Empty desc/schema fall back to t's.
func DecorateTool(t actool.CoreTool, desc string, schema map[string]any) actool.CoreTool {
	if desc == "" {
		desc = t.Description()
	}
	if len(schema) == 0 {
		schema = t.InputSchema()
	}
	return &overriddenTool{CoreTool: t, desc: desc, schema: schema}
}

// overriddenTool is a CoreTool decorator: it embeds the original (so all behavioral
// methods — Prompt/IsReadOnly/IsConcurrencySafe/CheckPermissions/Name — delegate)
// and overrides only the model-facing description/schema plus default injection.
type overriddenTool struct {
	actool.CoreTool
	desc   string
	schema map[string]any
}

func (o *overriddenTool) Description() string         { return o.desc }
func (o *overriddenTool) InputSchema() map[string]any { return o.schema }

func (o *overriddenTool) Call(ctx context.Context, in json.RawMessage, tc *actool.ToolContext) (actool.Result, error) {
	return o.CoreTool.Call(ctx, injectDefaults(in, o.schema), tc)
}

// injectDefaults fills scalar parameter defaults declared in the (possibly edited)
// schema into the input JSON whenever the model omitted the field or left it empty/
// null. Structure (names/types/required) is untouched — only缺省值 are merged in.
func injectDefaults(in json.RawMessage, schema map[string]any) json.RawMessage {
	defs := scalarDefaults(schema)
	if len(defs) == 0 {
		return in
	}
	m := map[string]json.RawMessage{}
	if len(in) > 0 {
		if err := json.Unmarshal(in, &m); err != nil {
			return in // non-object input: don't touch it
		}
	}
	changed := false
	for k, dv := range defs {
		if cur, ok := m[k]; !ok || isEmptyJSON(cur) {
			m[k] = dv
			changed = true
		}
	}
	if !changed {
		return in
	}
	b, err := json.Marshal(m)
	if err != nil {
		return in
	}
	return b
}

// scalarDefaults extracts properties[k]["default"] for scalar params (string/
// integer/number/boolean). Array/object defaults are skipped: merging them is
// ambiguous and not worth the surprise.
func scalarDefaults(schema map[string]any) map[string]json.RawMessage {
	props, _ := schema["properties"].(map[string]any)
	if len(props) == 0 {
		return nil
	}
	out := map[string]json.RawMessage{}
	for name, raw := range props {
		p, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		dv, ok := p["default"]
		if !ok || dv == nil {
			continue
		}
		switch p["type"] {
		case "string", "integer", "number", "boolean":
			if b, err := json.Marshal(dv); err == nil {
				out[name] = b
			}
		}
	}
	return out
}

func isEmptyJSON(raw json.RawMessage) bool {
	s := string(raw)
	return s == "null" || s == `""`
}
