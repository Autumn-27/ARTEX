package agent

import (
	"testing"

	"github.com/Autumn-27/artex/db"
)

// helper: intent/fact node
func intent(id int64, state string) cgNode { return cgNode{ID: id, Kind: db.KindIntent, State: state} }
func fact(id int64) cgNode                 { return cgNode{ID: id, Kind: db.KindFact, State: "confirmed"} }
func yields(from, to int64) cgEdge         { return cgEdge{From: from, Rel: db.RelYields, To: to} }
func derived(from, to int64) cgEdge        { return cgEdge{From: from, Rel: db.RelDerivedFrom, To: to} }

func ptr(v int64) *int64 { return &v }

// §附 快照1: a→b→c, b→d, with d live (running) and c settled/inactive.
// Expected: d/b/a hot (b kept hot by the live b→d branch); c is a cold candidate
// but an isolated singleton → not folded.
func TestHotCold_AnyLiveBranchKeepsChainHot(t *testing.T) {
	// a(intent) → b(intent) → c(fact); b → d(intent, running)
	nodes := []cgNode{intent(1, "done"), intent(2, "done"), fact(3), intent(4, "running")}
	edges := []cgEdge{derived(1, 2), yields(2, 3), derived(2, 4)}
	g := newColdGraph(nodes, edges)
	hot := g.hotSet()
	for _, id := range []int64{1, 2, 4} {
		if !hot[id] {
			t.Fatalf("node %d should be hot (ancestor of / is live intent 4)", id)
		}
	}
	if hot[3] {
		t.Fatalf("node 3 (dead leaf c) should be cold")
	}
	structCold := g.structuralCold(hot)
	if !structCold[3] || len(structCold) != 1 {
		t.Fatalf("only node 3 should be structurally cold, got %v", structCold)
	}
}

// §附 快照2: once d also finishes, a/b/c/d are all cold and connected → one block.
func TestHotCold_WholeChainFoldsWhenAllSettled(t *testing.T) {
	nodes := []cgNode{intent(1, "done"), intent(2, "done"), fact(3), intent(4, "done")}
	edges := []cgEdge{derived(1, 2), yields(2, 3), derived(2, 4)}
	g := newColdGraph(nodes, edges)
	hot := g.hotSet()
	if len(hot) != 0 {
		t.Fatalf("nothing should be hot once all settled, got %v", hot)
	}
	structCold := g.structuralCold(hot)
	// all stamped R+ rounds ago
	stamps := map[int64]*int64{1: ptr(1), 2: ptr(1), 3: ptr(1), 4: ptr(1)}
	elig := g.eligibleCold(structCold, stamps, 100, defaultColdParams())
	if len(elig) != 4 {
		t.Fatalf("all 4 nodes should be eligible cold, got %d", len(elig))
	}
	blocks := g.group(elig, defaultColdParams())
	if len(blocks) != 1 || len(blocks[0].Members) != 4 {
		t.Fatalf("expected one 4-member block, got %+v", blocks)
	}
}

// §2.3 debounce: a freshly-cooled node (stamp too recent) is not yet eligible.
func TestDebounce_RecentlyCooledNotEligible(t *testing.T) {
	nodes := []cgNode{intent(1, "done"), fact(2)}
	edges := []cgEdge{yields(1, 2)}
	g := newColdGraph(nodes, edges)
	structCold := g.structuralCold(g.hotSet())
	stamps := map[int64]*int64{1: ptr(98), 2: ptr(98)} // cooled at round 98
	elig := g.eligibleCold(structCold, stamps, 100, defaultColdParams())
	if len(elig) != 0 {
		t.Fatalf("nodes cooled only 2 rounds ago (<R=6) must not be eligible, got %v", elig)
	}
	elig = g.eligibleCold(structCold, stamps, 104, defaultColdParams()) // now 6 rounds
	if len(elig) != 2 {
		t.Fatalf("after R rounds both should be eligible, got %v", elig)
	}
}

// §3.1: a SETTLED hub kept hot only by ancestry to a live descendant (the real
// grap.log shape — exhausted/done intents with one live branch), whose OTHER
// children are flat dead leaves. Those leaves share the hot hub as parent, have
// no cold↔cold edges, yet must group (not stay singletons). Note: were the hub
// itself live (running), rule 1 would force its facts hot — that is a different
// case; here the hub is exhausted and hot only via the 50→77 live branch.
func TestGrouping_SharedParentRescuesFlatFanout(t *testing.T) {
	// hub(50) exhausted, hot via live descendant 77; dead cold facts 51..54.
	nodes := []cgNode{intent(50, "exhausted"), intent(77, "running")}
	edges := []cgEdge{derived(50, 77)}
	for id := int64(51); id <= 54; id++ {
		nodes = append(nodes, fact(id))
		edges = append(edges, yields(50, id))
	}
	g := newColdGraph(nodes, edges)
	hot := g.hotSet()
	if !hot[50] || !hot[77] {
		t.Fatalf("hub 50 (ancestor of live 77) and live 77 must be hot")
	}
	structCold := g.structuralCold(hot)
	stamps := map[int64]*int64{}
	for id := int64(51); id <= 54; id++ {
		stamps[id] = ptr(1)
	}
	elig := g.eligibleCold(structCold, stamps, 100, defaultColdParams())
	blocks := g.group(elig, defaultColdParams())
	// rule① alone would leave 51..54 as 4 singletons; rule② groups them into 1.
	if len(blocks) != 1 {
		t.Fatalf("expected 1 shared-parent block, got %d: %+v", len(blocks), blocks)
	}
	if len(blocks[0].Members) != 4 {
		t.Fatalf("block should hold all 4 dead leaves, got %v", blocks[0].Members)
	}
	// the hot hub is an anchor, never a member.
	if len(blocks[0].Anchors) != 1 || blocks[0].Anchors[0] != 50 {
		t.Fatalf("hub 50 should be the sole anchor, got %v", blocks[0].Anchors)
	}
	for _, m := range blocks[0].Members {
		if m == 50 {
			t.Fatalf("hub 50 must not be a member")
		}
	}
}

// §3①: a lone cold child under a hot parent stays an unfolded singleton.
func TestGrouping_LoneColdChildStaysSingleton(t *testing.T) {
	nodes := []cgNode{intent(50, "running"), intent(77, "running"), fact(51)}
	edges := []cgEdge{derived(50, 77), yields(50, 51)}
	g := newColdGraph(nodes, edges)
	structCold := g.structuralCold(g.hotSet())
	elig := g.eligibleCold(structCold, map[int64]*int64{51: ptr(1)}, 100, defaultColdParams())
	blocks := g.group(elig, defaultColdParams())
	if len(blocks) != 0 {
		t.Fatalf("a single cold leaf must not fold, got %+v", blocks)
	}
}

// §5.3: signature is stable under reordering and changes when a member's
// content_version bumps.
func TestSignature_StableAndVersionSensitive(t *testing.T) {
	b := block{Members: []int64{12, 28, 41}, Anchors: []int64{50}}
	cv := map[int64]int{12: 0, 28: 0, 41: 0, 50: 0}
	s1 := blockSignature(b, cv)
	// same members, same versions → same signature
	if s1 != blockSignature(block{Members: []int64{12, 28, 41}, Anchors: []int64{50}}, cv) {
		t.Fatalf("signature must be deterministic")
	}
	// bump a member version → signature changes
	cv2 := map[int64]int{12: 0, 28: 1, 41: 0, 50: 0}
	if s1 == blockSignature(b, cv2) {
		t.Fatalf("signature must change when a member content_version changes")
	}
	// bump anchor version → signature changes (anchor summary affects body)
	cv3 := map[int64]int{12: 0, 28: 0, 41: 0, 50: 1}
	if s1 == blockSignature(b, cv3) {
		t.Fatalf("signature must change when an anchor content_version changes")
	}
}

// computeStampOps: stamp on first cool, never re-stamp, clear on revival.
func TestStampOps(t *testing.T) {
	structCold := map[int64]bool{1: true, 2: true}
	coldSince := map[int64]*int64{2: ptr(5), 3: ptr(4)} // 2 already stamped; 3 stamped but revived
	ops := computeStampOps(structCold, coldSince, 10)
	got := map[int64]stampOp{}
	for _, o := range ops {
		got[o.ID] = o
	}
	if o, ok := got[1]; !ok || !o.Set || o.Round != 10 {
		t.Fatalf("node 1 should be stamped at round 10, got %+v", got[1])
	}
	if _, ok := got[2]; ok {
		t.Fatalf("node 2 already stamped, must not be re-stamped")
	}
	if o, ok := got[3]; !ok || o.Set {
		t.Fatalf("node 3 revived (not cold) → stamp must be cleared, got %+v", got[3])
	}
}
