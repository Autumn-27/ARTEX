package agent

import (
	"testing"

	actool "github.com/Autumn-27/norma/tool"
)

// hasRelatedParam reports whether insert_assets' schema still advertises the
// per-item `related` parameter (properties.assets.items.properties.related).
func hasRelatedParam(tool actool.CoreTool) bool {
	props, ok := nestedMap(tool.InputSchema(), "properties", "assets", "items", "properties")
	if !ok {
		return false
	}
	_, ok = props["related"]
	return ok
}

// TestStripCoverageParams verifies the insert_assets `related` param is visible
// while coverage is on and hidden once it's off, and that stripping never mutates
// the original tool's schema (deep copy).
func TestStripCoverageParams(t *testing.T) {
	ts := NewToolSet(nil, "")
	insert := ts.insertAssets()
	if !hasRelatedParam(insert) {
		t.Fatal("insert_assets 应默认带 related 参数")
	}

	// Enabled (default): schema unchanged, related still present.
	ts.SetCoverageEnabled(true)
	if got := ts.StripCoverageParams([]actool.CoreTool{insert}); !hasRelatedParam(got[0]) {
		t.Error("覆盖度开启时不应剔除 related 参数")
	}

	// Disabled: related stripped from the model-facing schema.
	ts.SetCoverageEnabled(false)
	out := ts.StripCoverageParams([]actool.CoreTool{insert})
	if hasRelatedParam(out[0]) {
		t.Error("覆盖度关闭时应隐藏 related 参数")
	}
	// The original tool must be untouched (deep copy, no shared-map mutation).
	if !hasRelatedParam(insert) {
		t.Error("StripCoverageParams 不应修改原始 tool 的 schema")
	}
}
