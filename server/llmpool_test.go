package server

import "testing"

// The status list drives the "轮询顺序" strip in the UI, so it has to match the
// order PoolProfiles actually runs: active first, then priority DESC, then id ASC
// (input arrives id-ordered, so equal priorities must keep their relative order).
func TestSortPoolStatusMatchesChainOrder(t *testing.T) {
	in := []LLMPoolMemberStatus{
		{ProfileID: "1", Name: "low", Priority: 0},
		{ProfileID: "2", Name: "active", Priority: 0, Active: true},
		{ProfileID: "3", Name: "high", Priority: 10},
		{ProfileID: "4", Name: "mid-a", Priority: 5},
		{ProfileID: "5", Name: "mid-b", Priority: 5},
	}
	sortPoolStatus(in)

	want := []string{"active", "high", "mid-a", "mid-b", "low"}
	for i, w := range want {
		if in[i].Name != w {
			got := make([]string, len(in))
			for j, m := range in {
				got[j] = m.Name
			}
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
}

// The active profile heads the chain no matter how low its own priority is —
// that's the documented precedence, and the UI must not imply otherwise.
func TestActiveProfileHeadsStatusList(t *testing.T) {
	in := []LLMPoolMemberStatus{
		{ProfileID: "1", Name: "loud", Priority: 999},
		{ProfileID: "2", Name: "active", Priority: -5, Active: true},
	}
	sortPoolStatus(in)
	if in[0].Name != "active" {
		t.Fatalf("head = %q, want the active profile regardless of priority", in[0].Name)
	}
}
