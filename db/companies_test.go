package db

import (
	"testing"
)

// cleanup helpers to remove test data
func cleanupCompany(d *DB, id int64) {
	d.Exec(`DELETE FROM company_scope WHERE company_id = $1`, id)
	d.Exec(`DELETE FROM companies WHERE id = $1`, id)
}

func TestCompanyUpsertAndGet(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer d.Close()
	cs := d.Companies()

	id, created, err := cs.UpsertCompany("Test Corp", "https://example.com/logo.png")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupCompany(d, id)
	if !created {
		t.Error("first upsert should report created=true")
	}

	// duplicate: same nkey, should not create new
	id2, created2, err := cs.UpsertCompany("Test Corp", "")
	if err != nil {
		t.Fatal(err)
	}
	if id2 != id {
		t.Errorf("dedup failed: %d != %d", id2, id)
	}
	if created2 {
		t.Error("second upsert should report created=false")
	}

	c, err := cs.GetCompany(id)
	if err != nil || c == nil {
		t.Fatalf("GetCompany: %v", err)
	}
	if c.Name != "Test Corp" {
		t.Errorf("name: %q", c.Name)
	}

	// GetCompanyByName
	c2, err := cs.GetCompanyByName("test corp") // normalised
	if err != nil || c2 == nil {
		t.Fatalf("GetCompanyByName: %v", err)
	}
	if c2.ID != id {
		t.Errorf("GetCompanyByName id mismatch: %d vs %d", c2.ID, id)
	}
}

func TestCompanyScope(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer d.Close()
	cs := d.Companies()

	id, _, err := cs.UpsertCompany("ScopeTestCorp", "")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupCompany(d, id)

	lines := []string{"example.com", "192.168.1.0/24", "10.0.0.1"}
	added, skipped, invalid, errs := cs.AddScope(id, lines, "test")
	if added != 3 {
		t.Errorf("want 3 added, got %d (errs: %v)", added, errs)
	}
	if skipped != 0 || invalid != 0 {
		t.Errorf("unexpected skipped=%d invalid=%d", skipped, invalid)
	}

	// Adding again should skip (duplicate)
	added2, skipped2, invalid2, _ := cs.AddScope(id, lines, "test")
	if added2 != 0 || skipped2 != 3 {
		t.Errorf("want 0 added 3 skipped, got %d added %d skipped %d invalid", added2, skipped2, invalid2)
	}

	scope, err := cs.GetScope(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(scope) != 3 {
		t.Errorf("want 3 scope rules, got %d", len(scope))
	}
}

func TestCompanyScopeInvalid(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer d.Close()
	cs := d.Companies()

	id, _, err := cs.UpsertCompany("InvalidScopeCorp", "")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupCompany(d, id)

	// TLD-only domains and overly broad CIDRs should be rejected
	lines := []string{"com", "1.2.3.4/8"} // rejected
	added, _, invalid, _ := cs.AddScope(id, lines, "test")
	if added != 0 {
		t.Errorf("want 0 added for invalid lines, got %d", added)
	}
	if invalid != 2 {
		t.Errorf("want 2 invalid, got %d", invalid)
	}
}

func TestResolveCompany(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer d.Close()
	cs := d.Companies()

	id, _, err := cs.UpsertCompany("ResolveCorp", "")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupCompany(d, id)

	cs.AddScope(id, []string{"resolve-test.io", "10.20.0.0/16"}, "test")

	// domain match
	cid, err := cs.ResolveCompany("resolve-test.io", "")
	if err != nil || cid == nil || *cid != id {
		t.Errorf("domain resolve: want %d, got %v (err %v)", id, cid, err)
	}

	// no match
	cid2, err := cs.ResolveCompany("notinscope.com", "")
	if err != nil || cid2 != nil {
		t.Errorf("no-match: want nil, got %v", cid2)
	}

	// IP/CIDR match
	cid3, err := cs.ResolveCompany("", "10.20.5.1")
	if err != nil || cid3 == nil || *cid3 != id {
		t.Errorf("cidr resolve: want %d, got %v (err %v)", id, cid3, err)
	}

	// IP outside CIDR
	cid4, err := cs.ResolveCompany("", "10.30.0.1")
	if err != nil || cid4 != nil {
		t.Errorf("cidr no-match: want nil, got %v", cid4)
	}
}

func TestUpdateScope(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer d.Close()
	cs := d.Companies()

	id, _, err := cs.UpsertCompany("UpdateScopeCorp", "")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupCompany(d, id)

	cs.AddScope(id, []string{"old-domain.com"}, "initial")

	// UpdateScope replaces
	added, invalid, errs := cs.UpdateScope(id, []string{"new-domain.com"}, "replacement")
	if added != 1 || invalid != 0 || len(errs) != 0 {
		t.Errorf("UpdateScope: added=%d invalid=%d errs=%v", added, invalid, errs)
	}

	scope, _ := cs.GetScope(id)
	if len(scope) != 1 || scope[0].Domain != "new-domain.com" {
		t.Errorf("UpdateScope: expected new-domain.com only, got %+v", scope)
	}
}

func TestDeleteCompany(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer d.Close()
	cs := d.Companies()

	id, _, err := cs.UpsertCompany("DeleteMeCorp", "")
	if err != nil {
		t.Fatal(err)
	}
	cs.AddScope(id, []string{"deletetest.com"}, "test")

	if err := cs.DeleteCompany(id); err != nil {
		t.Fatal(err)
	}
	c, err := cs.GetCompany(id)
	if err != nil || c != nil {
		t.Error("expected company to be gone")
	}
	// scope should be cascade-deleted
	scope, _ := cs.GetScope(id)
	if len(scope) != 0 {
		t.Errorf("expected scope cascade-deleted, got %d rules", len(scope))
	}
}

func TestRecomputeAttribution(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer d.Close()
	cs := d.Companies()
	as := d.Assets()

	id, _, err := cs.UpsertCompany("AttributeTestCorp", "")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupCompany(d, id)
	defer d.Exec(`DELETE FROM assets WHERE root_domain = 'attr-test.com'`)

	// insert asset before adding scope
	assetID, err := as.UpsertRootDomain(UpsertRootDomainReq{Domain: "attr-test.com"})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Exec(`DELETE FROM assets WHERE id = $1`, assetID)

	// asset should not be attributed yet
	var companyID *int64
	d.QueryRow(`SELECT company_id FROM assets WHERE id = $1`, assetID).Scan(&companyID)
	if companyID != nil {
		t.Error("expected no company before scope added")
	}

	// add scope and recompute
	cs.AddScope(id, []string{"attr-test.com"}, "test")
	if err := cs.RecomputeAttribution(); err != nil {
		t.Fatal(err)
	}

	d.QueryRow(`SELECT company_id FROM assets WHERE id = $1`, assetID).Scan(&companyID)
	if companyID == nil || *companyID != id {
		t.Errorf("RecomputeAttribution: expected company %d, got %v", id, companyID)
	}
}

func TestListCompanies(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer d.Close()
	cs := d.Companies()

	id, _, err := cs.UpsertCompany("ListTestCorp", "")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupCompany(d, id)

	companies, err := cs.ListCompanies()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range companies {
		if c.ID == id {
			found = true
		}
	}
	if !found {
		t.Error("ListCompanies: created company not found")
	}
}
