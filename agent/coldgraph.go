package agent

// cold-digest §2/§3: pure graph algorithms for cold-node compression.
//
// This file is deliberately free of any DB or LLM dependency so the hot/cold
// judgment, connectivity grouping (§3) and the same-parent singleton rescue
// (§3.1) can be unit-tested in isolation. Callers translate db.Node/db.Edge into
// the light cgNode/cgEdge structs and feed the per-node bookkeeping (cold_since
// stamps, content versions) alongside.
//
// Edge direction convention (matches db + agent/tools.go graphOverviewData):
// every edge From→To means From is the parent/upstream and To the child/
// downstream, for ALL relations (yields: intent→fact, derived_from/spawns:
// parent→child). "Downstream" therefore follows From→To.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"

	"github.com/Autumn-27/artex/db"
)

// cgNode is the minimal node view the cold-graph algorithms need.
type cgNode struct {
	ID    int64
	Kind  string
	State string
}

// cgEdge is one exploration edge (From = parent/upstream, To = child/downstream).
type cgEdge struct {
	From int64
	Rel  string
	To   int64
}

// coldParams are the tunable thresholds (cold-digest §7).
type coldParams struct {
	R int // debounce: a node must be continuously inactive ≥R planner rounds (§7 R=6)
	K int // min block size to fold; K=2 skips only degenerate singletons (§7 K=2)
}

func defaultColdParams() coldParams { return coldParams{R: 6, K: 2} }

// coldGraph is an in-memory adjacency view over real exploration edges. The
// derived view layer (kind=digest nodes, rel=covers edges) is filtered out at
// construction so it can never distort causal reachability or grouping (§2/§3).
type coldGraph struct {
	nodes    map[int64]cgNode
	children map[int64][]int64 // From → [To]   (downstream)
	parents  map[int64][]int64 // To   → [From] (upstream)
}

func newColdGraph(nodes []cgNode, edges []cgEdge) *coldGraph {
	g := &coldGraph{
		nodes:    make(map[int64]cgNode, len(nodes)),
		children: map[int64][]int64{},
		parents:  map[int64][]int64{},
	}
	for _, n := range nodes {
		g.nodes[n.ID] = n
	}
	for _, e := range edges {
		if e.Rel == db.RelCovers { // derived view layer, not exploration causality (§2/§3)
			continue
		}
		if _, ok := g.nodes[e.From]; !ok {
			continue
		}
		if _, ok := g.nodes[e.To]; !ok {
			continue
		}
		g.children[e.From] = append(g.children[e.From], e.To)
		g.parents[e.To] = append(g.parents[e.To], e.From)
	}
	return g
}

// isLiveIntent reports whether a node is a not-yet-settled intent — the frontier
// that keeps its ancestors hot. paused counts as live (it may still resume);
// settled = done/blocked/exhausted/stopped.
func isLiveIntent(n cgNode) bool {
	if n.Kind != db.KindIntent {
		return false
	}
	switch n.State {
	case "open", "running", "paused":
		return true
	}
	return false
}

// foldableKind reports whether a node kind is eligible for folding at all (§2:
// only fact and settled intent; finding/goal/hint/begin/digest never fold).
func foldableKind(k string) bool { return k == db.KindIntent || k == db.KindFact }

// hotSet computes the hot nodes (§2 rule 1+2): a node is hot iff it can reach a
// live intent by going downstream (it is an ancestor of a live intent), OR it is
// a live intent, OR it is a direct child of a live intent (rule 1: an open/
// running intent's freshly produced facts stay hot). Everything else is cold-
// eligible. "Any live branch keeps the whole chain hot" falls out of ancestor
// marking. Iterative (no recursion) to tolerate deep chains and cycles.
func (g *coldGraph) hotSet() map[int64]bool {
	hot := map[int64]bool{}
	var stack []int64
	for _, n := range g.nodes {
		if isLiveIntent(n) {
			stack = append(stack, n.ID)
		}
	}
	// Walk upstream from every live intent, marking all ancestors hot.
	for len(stack) > 0 {
		id := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if hot[id] {
			continue
		}
		hot[id] = true
		stack = append(stack, g.parents[id]...)
	}
	// A live intent's direct children (its fresh facts) stay hot (rule 1).
	for _, n := range g.nodes {
		if isLiveIntent(n) {
			for _, c := range g.children[n.ID] {
				hot[c] = true
			}
		}
	}
	return hot
}

// structuralCold is the set of foldable nodes that are currently not hot — i.e.
// settled + blood-inactive (§2 rules 1+2), before the ≥R debounce is applied.
func (g *coldGraph) structuralCold(hot map[int64]bool) map[int64]bool {
	cold := map[int64]bool{}
	for id, n := range g.nodes {
		if foldableKind(n.Kind) && !hot[id] {
			cold[id] = true
		}
	}
	return cold
}

// stampOp is one cold_since_round bookkeeping change (§2.3): Set=true stamps the
// round a node went cold; Set=false clears the stamp (the node revived / turned
// hot again).
type stampOp struct {
	ID    int64
	Set   bool
	Round int64
}

// computeStampOps derives the cold_since_round updates for this round. It stamps
// a node the round it FIRST goes cold (empty→round_no) and clears the stamp when
// it is no longer cold. It never re-stamps an already-stamped cold node — that is
// what preserves "how long it has been cold" (§2.3: measure the round it turned
// cold, not the round it last turned hot). coldSince maps node id → stamp (nil =
// unstamped / hot).
func computeStampOps(structCold map[int64]bool, coldSince map[int64]*int64, roundNo int64) []stampOp {
	var ops []stampOp
	seen := map[int64]bool{}
	for id := range structCold {
		seen[id] = true
		if coldSince[id] == nil {
			ops = append(ops, stampOp{ID: id, Set: true, Round: roundNo})
		}
	}
	// Clear stamps on nodes that are stamped but no longer cold (revived/hot).
	for id, cs := range coldSince {
		if cs != nil && !seen[id] {
			ops = append(ops, stampOp{ID: id, Set: false})
		}
	}
	sort.Slice(ops, func(i, j int) bool { return ops[i].ID < ops[j].ID })
	return ops
}

// eligibleCold narrows structuralCold to nodes that have been continuously cold
// for ≥R rounds (§2.3 / §3②). A node with no stamp, or one that has not yet
// aged R rounds, is held in the hot region a while longer (bias to conservative).
func (g *coldGraph) eligibleCold(structCold map[int64]bool, coldSince map[int64]*int64, roundNo int64, p coldParams) map[int64]bool {
	out := map[int64]bool{}
	for id := range structCold {
		if cs := coldSince[id]; cs != nil && roundNo-*cs >= int64(p.R) {
			out[id] = true
		}
	}
	return out
}

// block is a group of cold nodes to fold into one digest, plus the external
// parent nodes that anchor them (§3.1 "父作锚不作成员"): anchors are fed to the
// compressor as context but never become members / never get a covers edge.
type block struct {
	Members []int64 // sorted; the nodes this digest covers
	Anchors []int64 // sorted; external (non-member) parents, context only
}

// group partitions `set` into foldable blocks (§3 + §3.1). Two passes of
// union-find:
//
//	rule ①  connect cold nodes joined by a real exploration edge (§3);
//	rule ②  connect the LEFTOVER singletons that share a common direct parent
//	        (§3.1 — rescues the "hot hub + flat dead leaves" fan-out), without
//	        disturbing any already-formed ≥2 block.
//
// Only components of size ≥K survive (§3① skips degenerate singletons).
func (g *coldGraph) group(set map[int64]bool, p coldParams) []block {
	uf := newUnionFind(set)
	// rule ①: real cold↔cold edges.
	for from := range set {
		for _, to := range g.children[from] {
			if set[to] {
				uf.union(from, to)
			}
		}
	}
	// rule ②: leftover singletons sharing a common parent.
	comps := uf.components()
	byParent := map[int64][]int64{}
	for _, ids := range comps {
		if len(ids) != 1 {
			continue // only rescue singletons; never re-shuffle ≥2 blocks
		}
		s := ids[0]
		for _, par := range g.parents[s] {
			if g.nodes[par].Kind == db.KindDigest { // anchor must be a real node, not a digest
				continue
			}
			byParent[par] = append(byParent[par], s)
		}
	}
	for _, sibs := range byParent {
		if len(sibs) < 2 {
			continue // a lone cold child under a parent stays a true singleton (§3①)
		}
		for i := 1; i < len(sibs); i++ {
			uf.union(sibs[0], sibs[i])
		}
	}
	// Emit surviving components as blocks, each with its external-parent anchors.
	comps = uf.components()
	var blocks []block
	for _, ids := range comps {
		if len(ids) < p.K {
			continue
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		memberSet := make(map[int64]bool, len(ids))
		for _, m := range ids {
			memberSet[m] = true
		}
		anchorSet := map[int64]bool{}
		for _, m := range ids {
			for _, par := range g.parents[m] {
				if memberSet[par] {
					continue
				}
				pn, ok := g.nodes[par]
				if !ok || pn.Kind == db.KindDigest {
					continue
				}
				anchorSet[par] = true
			}
		}
		anchors := make([]int64, 0, len(anchorSet))
		for a := range anchorSet {
			anchors = append(anchors, a)
		}
		sort.Slice(anchors, func(i, j int) bool { return anchors[i] < anchors[j] })
		blocks = append(blocks, block{Members: ids, Anchors: anchors})
	}
	// Deterministic order: by smallest member id.
	sort.Slice(blocks, func(i, j int) bool { return blocks[i].Members[0] < blocks[j].Members[0] })
	return blocks
}

// blockSignature is the change-detection key (§5.3): a hash over the sorted
// member ids + each member's content_version, plus the anchor ids + versions
// (so an anchor's summary/state change also invalidates the cached body). A
// re-compaction whose block matches an existing active digest's signature
// reuses the stored body and skips the LLM entirely.
func blockSignature(b block, contentVer map[int64]int) string {
	h := sha256.New()
	for _, m := range b.Members {
		fmt.Fprintf(h, "m:%d:%d;", m, contentVer[m])
	}
	for _, a := range b.Anchors {
		fmt.Fprintf(h, "a:%d:%d;", a, contentVer[a])
	}
	return hex.EncodeToString(h.Sum(nil))
}

// --- union-find ---

type unionFind struct{ parent map[int64]int64 }

func newUnionFind(set map[int64]bool) *unionFind {
	uf := &unionFind{parent: make(map[int64]int64, len(set))}
	for id := range set {
		uf.parent[id] = id
	}
	return uf
}

func (u *unionFind) find(x int64) int64 {
	for u.parent[x] != x {
		u.parent[x] = u.parent[u.parent[x]]
		x = u.parent[x]
	}
	return x
}

func (u *unionFind) union(a, b int64) {
	ra, rb := u.find(a), u.find(b)
	if ra != rb {
		u.parent[ra] = rb
	}
}

func (u *unionFind) components() map[int64][]int64 {
	out := map[int64][]int64{}
	for id := range u.parent {
		r := u.find(id)
		out[r] = append(out[r], id)
	}
	return out
}
