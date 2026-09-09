package agent

// cold-digest §6: graph_overview folding + the restore tools.
//
//	coldDigestOverview — builds the folded cold region for graph_overview:
//	  cold_digests (flat {id, body, member_count}) and cold_index (§6.2, the
//	  per-asset directory that collapses independent directions).
//	expand_digest(id)     — level-1 restore: a digest's member compact list.
//	expand_index(asset_id) — level-0 restore: the digests under one asset.

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/Autumn-27/artex/db"
	actool "github.com/Autumn-27/norma/tool"
)

// coldDigestOverview returns the folded cold region for graph_overview: the flat
// digest bodies and the asset-grouped index (§6.1/§6.2). covered is unused here
// (kept for symmetry with the caller's coverage computation).
func (t *ToolSet) coldDigestOverview() (digests []map[string]any, index []map[string]any) {
	ads, err := t.ts.ActiveDigests()
	if err != nil || len(ads) == 0 {
		return nil, nil
	}
	memByDigest := map[int64][]int64{}
	var allMembers []int64
	for _, d := range ads {
		ms, _ := t.ts.DigestMembers(d.ID)
		memByDigest[d.ID] = ms
		allMembers = append(allMembers, ms...)
	}
	assetsByNode, _ := t.ts.NodeAssets(allMembers)

	digests = make([]map[string]any, 0, len(ads))
	for _, d := range ads {
		var p struct {
			Body string `json:"body"`
		}
		_ = json.Unmarshal(d.Payload, &p)
		digests = append(digests, map[string]any{
			"id":           d.ID,
			"body":         p.Body,
			"member_count": len(memByDigest[d.ID]),
		})
	}

	// index (§6.2): asset → digests. Each digest lands in EXACTLY ONE bucket — its
	// representative asset = the asset anchored on the most of its members (mode;
	// tie-break lowest id). This is what makes the index converge: bucketing a digest
	// into every asset its members touch would duplicate it across dozens of buckets
	// and blow up the top-level count instead of shrinking it (asset_ids are fine-
	// grained — a real task has ~144 of them). A digest whose members anchor no asset
	// falls into the 0 bucket ("(未锚定资产)").
	byAsset := map[int64]map[int64]bool{} // asset id → set of digest ids
	assetSet := map[int64]bool{}
	for dID, ms := range memByDigest {
		counts := map[int64]int{}
		for _, m := range ms {
			for _, a := range assetsByNode[m] {
				counts[a]++
			}
		}
		rep, best := int64(0), 0
		for a, c := range counts {
			if c > best || (c == best && (rep == 0 || a < rep)) {
				rep, best = a, c
			}
		}
		if byAsset[rep] == nil {
			byAsset[rep] = map[int64]bool{}
		}
		byAsset[rep][dID] = true
		if rep != 0 {
			assetSet[rep] = true
		}
	}
	labels := map[int64]string{}
	if t.as != nil && len(assetSet) > 0 {
		ids := make([]int64, 0, len(assetSet))
		for a := range assetSet {
			ids = append(ids, a)
		}
		if assets, err := t.as.GetByIDs(ids); err == nil {
			for _, a := range assets {
				if v := assetValue(a); v != "" {
					labels[a.ID] = v
				}
			}
		}
	}
	index = make([]map[string]any, 0, len(byAsset))
	for a, dset := range byAsset {
		dids := make([]int64, 0, len(dset))
		for d := range dset {
			dids = append(dids, d)
		}
		sort.Slice(dids, func(i, j int) bool { return dids[i] < dids[j] })
		entry := map[string]any{"digest_ids": dids}
		if a == 0 {
			entry["asset"] = "(未锚定资产)"
		} else {
			entry["asset_id"] = a
			if l := labels[a]; l != "" {
				entry["asset"] = l
			} else {
				entry["asset"] = fmt.Sprintf("#%d", a)
			}
		}
		index = append(index, entry)
	}
	sort.Slice(index, func(i, j int) bool {
		return fmt.Sprint(index[i]["asset"]) < fmt.Sprint(index[j]["asset"])
	})
	return digests, index
}

// digestMemberEntry builds the compact per-member view expand_digest returns —
// same shape as recent_facts / recent_done_intents (§6.1 middle level). store is
// the digest's OWNING store (the current task, or a read-only source task §2).
func (t *ToolSet) digestMemberEntry(store *db.ExplorationStore, id int64) map[string]any {
	n, _ := store.GetNode(id)
	if n == nil {
		return map[string]any{"id": id, "missing": true}
	}
	m := compactNode(n)
	m["state"] = n.State
	var p map[string]any
	if json.Unmarshal(n.Payload, &p) == nil {
		if c, ok := p["confidence"].(string); ok && c != "" {
			m["confidence"] = c
		}
	}
	return m
}

// activeDigestBodies returns [{id, body, member_count}] for a store's active
// digests — the folded cold region as flat bodies (§6.1). Shared by the current
// task overview and the read-only related-task overview (§2 cross-task reuse).
func activeDigestBodies(store *db.ExplorationStore) []map[string]any {
	ads, err := store.ActiveDigests()
	if err != nil || len(ads) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(ads))
	for _, d := range ads {
		var p struct {
			Body string `json:"body"`
		}
		_ = json.Unmarshal(d.Payload, &p)
		ms, _ := store.DigestMembers(d.ID)
		out = append(out, map[string]any{"id": d.ID, "body": p.Body, "member_count": len(ms)})
	}
	return out
}

// hiddenMembersFor returns a predicate telling whether a member is hidden (folded
// into an active digest AND still cold) in the given store — so a source task's
// overview folds exactly the way that task folds itself (§2 cross-task: "当前任务
// 什么展示逻辑，关联任务就什么逻辑"). A revived (now hot) covered member is NOT
// hidden (§6 render-time revival check). Returns a never-hidden predicate when the
// store has no digests.
func hiddenMembersFor(store *db.ExplorationStore) func(int64) bool {
	covered, err := store.CoveredMembers()
	if err != nil || len(covered) == 0 {
		return func(int64) bool { return false }
	}
	var hot map[int64]bool
	if cg, _, err := loadColdGraph(store); err == nil {
		hot = cg.hotSet()
	}
	return func(id int64) bool { _, c := covered[id]; return c && !hot[id] }
}

// resolveDigest finds a digest node by id in the current task, else in a direct
// source task (read-only, §2). Returns the node, its owning store, and the source
// task id (0 = current task).
func (t *ToolSet) resolveDigest(id int64) (*db.Node, *db.ExplorationStore, int64) {
	if n, _ := t.ts.GetNode(id); n != nil && n.Kind == db.KindDigest {
		return n, t.ts, 0
	}
	srcs, _ := t.ts.DirectSourceStores()
	for _, s := range srcs {
		if n, _ := s.Store.GetNode(id); n != nil && n.Kind == db.KindDigest {
			return n, s.Store, s.Task.TaskID
		}
	}
	return nil, nil, 0
}

// expandDigest returns a digest's covered members as a compact list (§6.1). It is
// a distinct tool from node_detail because it returns a LIST of members, not one
// node's full detail.
func (t *ToolSet) expandDigest() actool.CoreTool {
	return writeTool("expand_digest",
		"展开一个 cold digest：返回它折叠的成员紧凑列表（id/summary/state/confidence），与概览 recent_facts/recent_done_intents 同形状。要某条完整细节/证据用 node_detail(member_id)。",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id": map[string]any{"type": "integer", "description": "digest 节点 id（来自概览 cold_digests / cold_index / covered_members）"},
			},
			"required": []any{"id"},
		},
		func(ctx context.Context, raw json.RawMessage) (actool.Result, error) {
			var in struct {
				ID int64 `json:"id"`
			}
			_ = json.Unmarshal(raw, &in)
			n, store, srcTaskID := t.resolveDigest(in.ID)
			if n == nil {
				return jsonResult(map[string]any{"error": fmt.Sprintf("#%d 不是 digest 节点（本任务或直接关联任务里都没找到）", in.ID)})
			}
			var p struct {
				Body string `json:"body"`
			}
			_ = json.Unmarshal(n.Payload, &p)
			members, _ := store.DigestMembers(in.ID)
			list := make([]map[string]any, 0, len(members))
			for _, m := range members {
				entry := t.digestMemberEntry(store, m)
				if srcTaskID > 0 { // 关联任务的成员：只读，带继承标记（§2）
					entry["inherited"] = true
					entry["source_task_id"] = srcTaskID
				}
				list = append(list, entry)
			}
			out := map[string]any{
				"id":      in.ID,
				"state":   n.State, // active / superseded
				"body":    p.Body,
				"members": list,
			}
			if srcTaskID > 0 {
				out["inherited"] = true
				out["source_task_id"] = srcTaskID
			}
			return jsonResult(out)
		})
}

// expandIndex returns the active digests under one asset (§6.2 level-0), each
// with its body + member count — so the planner can drill an asset directory down
// to its directions without reading every digest globally.
func (t *ToolSet) expandIndex() actool.CoreTool {
	return writeTool("expand_index",
		"展开冷区某个资产条目（来自概览 cold_index）：返回该资产名下的 cold digest 列表（id/body/member_count）。再往下看某个 digest 的成员用 expand_digest(id)。",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"asset_id": map[string]any{"type": "integer", "description": "资产 id（来自概览 cold_index 的 asset_id；传 0 或省略取未锚定资产桶）"},
			},
		},
		func(ctx context.Context, raw json.RawMessage) (actool.Result, error) {
			var in struct {
				AssetID int64 `json:"asset_id"`
			}
			_ = json.Unmarshal(raw, &in)
			ads, _ := t.ts.ActiveDigests()
			out := make([]map[string]any, 0)
			for _, d := range ads {
				members, _ := t.ts.DigestMembers(d.ID)
				assetsByNode, _ := t.ts.NodeAssets(members)
				match := in.AssetID == 0
				for _, m := range members {
					for _, a := range assetsByNode[m] {
						if a == in.AssetID {
							match = true
						}
					}
					if match {
						break
					}
				}
				// asset_id==0 means "unanchored bucket": include only digests with no asset.
				if in.AssetID == 0 {
					anchored := false
					for _, m := range members {
						if len(assetsByNode[m]) > 0 {
							anchored = true
							break
						}
					}
					match = !anchored
				}
				if !match {
					continue
				}
				var p struct {
					Body string `json:"body"`
				}
				_ = json.Unmarshal(d.Payload, &p)
				out = append(out, map[string]any{"id": d.ID, "body": p.Body, "member_count": len(members)})
			}
			return jsonResult(map[string]any{"asset_id": in.AssetID, "digests": out})
		})
}
