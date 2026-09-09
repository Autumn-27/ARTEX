package agent

import "testing"

func TestBalancedCap(t *testing.T) {
	cases := []struct {
		name                string
		done, facts, cap    int
		wantDone, wantFacts int
		wantTrunc           bool
	}{
		{"under cap", 15, 20, 60, 15, 20, false},
		{"exactly cap", 30, 30, 60, 30, 30, false},
		{"both large → even split", 86, 40, 60, 30, 30, true},
		{"intents dominate, facts small kept whole", 86, 5, 60, 55, 5, true},
		{"facts dominate, intents small kept whole", 3, 200, 60, 3, 57, true},
		{"both huge → half/half", 500, 500, 60, 30, 30, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotDone, gotFacts, trunc := balancedCap(c.done, c.facts, c.cap)
			if gotDone != c.wantDone || gotFacts != c.wantFacts || trunc != c.wantTrunc {
				t.Fatalf("balancedCap(%d,%d,%d) = (%d,%d,%v), want (%d,%d,%v)",
					c.done, c.facts, c.cap, gotDone, gotFacts, trunc, c.wantDone, c.wantFacts, c.wantTrunc)
			}
			if trunc && gotDone+gotFacts > c.cap {
				t.Fatalf("kept %d+%d exceeds cap %d", gotDone, gotFacts, c.cap)
			}
			// never starve a non-empty side to zero when it had items
			if trunc && c.facts > 0 && gotFacts == 0 {
				t.Fatalf("facts starved to 0")
			}
		})
	}
}
