package db

import (
	"fmt"
	"testing"
)

// TestParseScopeLine covers classification + guardrails without a DB.
func TestParseScopeLine(t *testing.T) {
	cases := []struct {
		in      string
		kind    string
		wantErr bool
	}{
		{"example.com", "domain", false},
		{"https://sub.example.com/path", "domain", false},
		{"1.2.3.4", "ip", false},
		{"10.0.0.0/8", "", true},       // over-broad IPv4 (< /16)
		{"198.51.100.0/24", "cidr", false},
		{"co.uk", "", true},            // bare public suffix
		{"not a host", "", true},
		{"1.2.3.1-1.2.3.9", "", true}, // ranges must be CIDR
	}
	for _, c := range cases {
		r, err := ParseScopeLine(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("ParseScopeLine(%q) want error, got %+v", c.in, r)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseScopeLine(%q) unexpected error: %v", c.in, err)
			continue
		}
		if r.Kind != c.kind {
			t.Errorf("ParseScopeLine(%q) kind=%q want %q", c.in, r.Kind, c.kind)
		}
	}
}

// TestCompanyScopeAttribution exercises the full loop against dev PG: create
// company (unique name), add scope, and verify auto-attribution at insert time,
// backfill of a pre-existing asset, CIDR + domain-suffix matching, and that
// out-of-scope assets stay unattributed.
func TestCompanyScopeAttribution(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	as := d.Assets()
	cs := d.Companies()

	var startMax int64
	if err := d.QueryRow(`SELECT COALESCE(MAX(id),0) FROM assets`).Scan(&startMax); err != nil {
		d.Close()
		t.Fatalf("startMax: %v", err)
	}
	t.Cleanup(func() {
		_, _ = d.Exec(`DELETE FROM assets WHERE id > $1`, startMax)
		d.Close()
	})

	uniq := startMax + 1
	root := fmt.Sprintf("scopetest%d.com", uniq)
	sub := "api." + root
	ipIn := "198.51.100.9"
	ipOut := "203.0.113.9"
	outDomain := fmt.Sprintf("other%d.net", uniq)

	cid, _, err := cs.UpsertCompany(fmt.Sprintf("ScopeCo %d", uniq), "")
	if err != nil {
		t.Fatalf("UpsertCompany: %v", err)
	}

	// a pre-existing asset (inserted BEFORE any scope) — must be back-filled.
	preID, err := as.UpsertSubdomain(UpsertSubdomainReq{Domain: sub})
	if err != nil {
		t.Fatalf("pre upsert: %v", err)
	}
	var preCompanyID *int64
	d.QueryRow(`SELECT company_id FROM assets WHERE id = $1`, preID).Scan(&preCompanyID)
	if preCompanyID != nil {
		t.Fatalf("pre-scope asset should be unattributed, got %v", *preCompanyID)
	}

	rules, invalid := ParseScopeLines(root + "\n198.51.100.0/24\nco.uk")
	if len(rules) != 2 {
		t.Fatalf("want 2 valid rules, got %d (%+v)", len(rules), rules)
	}
	if len(invalid) != 1 {
		t.Fatalf("want 1 invalid line, got %v", invalid)
	}
	cs.AddScope(cid, []string{root, "198.51.100.0/24"}, "unit test")

	mustCid := func(id int64, want int64, label string) {
		var cID *int64
		d.QueryRow(`SELECT company_id FROM assets WHERE id = $1`, id).Scan(&cID)
		if cID == nil {
			t.Fatalf("%s company_id = nil, want %d", label, want)
		}
		if *cID != want {
			t.Fatalf("%s company_id = %d, want %d", label, *cID, want)
		}
	}
	mustNil := func(id int64, label string) {
		var cID *int64
		d.QueryRow(`SELECT company_id FROM assets WHERE id = $1`, id).Scan(&cID)
		if cID != nil {
			t.Fatalf("%s should be unattributed, got %d", label, *cID)
		}
	}

	// backfill attributed the pre-existing subdomain (domain suffix match).
	mustCid(preID, cid, "pre-existing subdomain (backfill)")

	// insert-time attribution: ip in CIDR, another subdomain.
	ipInID, err := as.UpsertIP(UpsertIPReq{IP: ipIn})
	if err != nil {
		t.Fatalf("UpsertIP in: %v", err)
	}
	mustCid(ipInID, cid, "in-CIDR ip (insert-time)")

	sub2ID, err := as.UpsertSubdomain(UpsertSubdomainReq{Domain: "www." + root})
	if err != nil {
		t.Fatalf("UpsertSubdomain: %v", err)
	}
	mustCid(sub2ID, cid, "new subdomain (insert-time)")

	// out of scope stays unattributed.
	outID, err := as.UpsertRootDomain(UpsertRootDomainReq{Domain: outDomain})
	if err != nil {
		t.Fatalf("UpsertRootDomain out: %v", err)
	}
	mustNil(outID, "out-of-scope domain")

	ipOutID, err := as.UpsertIP(UpsertIPReq{IP: ipOut})
	if err != nil {
		t.Fatalf("UpsertIP out: %v", err)
	}
	mustNil(ipOutID, "out-of-CIDR ip")
}
