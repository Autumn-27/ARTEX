package db

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestAssetUpsertWaitsForCompanyScopeMutation(t *testing.T) {
	d, assets, companies := testSetup(t)
	defer d.Close()

	stamp := time.Now().UnixNano()
	domain := fmt.Sprintf("scope-lock-%d.example", stamp)
	oldCompany, _, err := companies.UpsertCompany(fmt.Sprintf("Scope Lock Old %d", stamp), "")
	if err != nil {
		t.Fatal(err)
	}
	newCompany, _, err := companies.UpsertCompany(fmt.Sprintf("Scope Lock New %d", stamp), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = d.Exec(`DELETE FROM assets WHERE domain = $1`, domain)
		_, _ = d.Exec(`DELETE FROM companies WHERE id IN ($1,$2)`, oldCompany, newCompany)
	})
	if added, _, invalid, validationErrors, err := companies.AddScopeInputsChecked(oldCompany, []ScopeInput{
		{Kind: "domain", Value: domain},
	}, "test"); err != nil || added != 1 || invalid != 0 || len(validationErrors) != 0 {
		t.Fatalf("seed old scope: added=%d invalid=%d validation=%v err=%v", added, invalid, validationErrors, err)
	}

	mutation, err := d.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer mutation.Rollback() //nolint:errcheck
	if err := lockCompanyScopeMutation(mutation); err != nil {
		t.Fatal(err)
	}
	if _, err := mutation.Exec(`DELETE FROM company_scope WHERE company_id=$1 AND kind='domain' AND domain=$2`, oldCompany, domain); err != nil {
		t.Fatal(err)
	}
	if _, err := insertScopeRuleTx(mutation, newCompany, ParsedScope{
		Kind: "domain", Domain: domain, Raw: domain,
	}, "test"); err != nil {
		t.Fatal(err)
	}

	started := make(chan struct{})
	type result struct {
		id  int64
		err error
	}
	resultCh := make(chan result, 1)
	go func() {
		close(started)
		id, err := assets.UpsertRootDomain(UpsertRootDomainReq{Domain: domain})
		resultCh <- result{id: id, err: err}
	}()
	<-started
	select {
	case got := <-resultCh:
		t.Fatalf("asset upsert escaped scope mutation lock: %+v", got)
	case <-time.After(100 * time.Millisecond):
	}

	if err := mutation.Commit(); err != nil {
		t.Fatal(err)
	}
	var got result
	select {
	case got = <-resultCh:
	case <-time.After(3 * time.Second):
		t.Fatal("asset upsert did not resume after scope mutation committed")
	}
	if got.err != nil {
		t.Fatal(got.err)
	}
	var companyID *int64
	if err := d.QueryRow(`SELECT company_id FROM assets WHERE id=$1`, got.id).Scan(&companyID); err != nil {
		t.Fatal(err)
	}
	if companyID == nil || *companyID != newCompany {
		t.Fatalf("asset retained stale company: got=%v want=%d", companyID, newCompany)
	}
}

func TestResolveAndRecomputeUseStableCompanyIDTieBreak(t *testing.T) {
	d, assets, companies := testSetup(t)
	defer d.Close()

	stamp := time.Now().UnixNano()
	first, _, err := companies.UpsertCompany(fmt.Sprintf("Tie Break First %d", stamp), "")
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := companies.UpsertCompany(fmt.Sprintf("Tie Break Second %d", stamp), "")
	if err != nil {
		t.Fatal(err)
	}
	want := min(first, second)
	domain := fmt.Sprintf("tie-break-%d.example", stamp)
	segment := uint64(stamp) & 0xffff
	network := fmt.Sprintf("2001:db8:%x:1::/64", segment)
	ip := fmt.Sprintf("2001:db8:%x:1::42", segment)
	icp := fmt.Sprintf("ICP-TIE-%d", stamp)
	appName := fmt.Sprintf("Tie Break App %d", stamp)
	t.Cleanup(func() {
		_, _ = d.Exec(`DELETE FROM assets WHERE domain=$1 OR ip=$2 OR app_name=$3`, domain, ip, appName)
		_, _ = d.Exec(`DELETE FROM companies WHERE id IN ($1,$2)`, first, second)
	})

	rules := []ScopeInput{
		{Kind: "domain", Value: domain},
		{Kind: "cidr", Value: network},
		{Kind: "icp", Value: icp},
	}
	for _, companyID := range []int64{second, first} {
		if added, _, invalid, validationErrors, err := companies.AddScopeInputsChecked(companyID, rules, "test"); err != nil || added != len(rules) || invalid != 0 || len(validationErrors) != 0 {
			t.Fatalf("add company %d rules: added=%d invalid=%d validation=%v err=%v", companyID, added, invalid, validationErrors, err)
		}
	}

	assertResolved := func(label string, got *int64, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s resolve: %v", label, err)
		}
		if got == nil || *got != want {
			t.Fatalf("%s resolve=%v want lowest company id %d", label, got, want)
		}
	}
	got, err := companies.ResolveCompany(domain, "")
	assertResolved("domain", got, err)
	got, err = companies.ResolveCompany("", ip)
	assertResolved("network", got, err)
	got, err = companies.ResolveCompanyWithICP("", "", icp)
	assertResolved("icp", got, err)

	rootID, err := assets.UpsertRootDomain(UpsertRootDomainReq{Domain: domain})
	if err != nil {
		t.Fatal(err)
	}
	ipID, err := assets.UpsertIP(UpsertIPReq{IP: ip})
	if err != nil {
		t.Fatal(err)
	}
	appID, err := assets.UpsertApp(UpsertAppReq{Name: appName, ICP: icp})
	if err != nil {
		t.Fatal(err)
	}
	assertAssets := func(stage string) {
		t.Helper()
		for _, id := range []int64{rootID, ipID, appID} {
			var got *int64
			if err := d.QueryRow(`SELECT company_id FROM assets WHERE id=$1`, id).Scan(&got); err != nil {
				t.Fatal(err)
			}
			if got == nil || *got != want {
				t.Fatalf("%s asset %d company=%v want=%d", stage, id, got, want)
			}
		}
	}
	assertAssets("live")
	if err := companies.RecomputeAttribution(); err != nil {
		t.Fatal(err)
	}
	assertAssets("recomputed")
}

func TestCompanyScopeLimitsAndCheckedErrors(t *testing.T) {
	boundary := strings.Repeat("界", MaxCompanyScopeRawRunes)
	if err := ValidateCompanyScopeInputBounds([]ScopeInput{{Kind: "keyword", Value: boundary}}); err != nil {
		t.Fatalf("exact raw rune boundary rejected: %v", err)
	}
	var validationErr *CompanyScopeValidationError
	if err := ValidateCompanyScopeInputBounds([]ScopeInput{{Kind: "keyword", Value: boundary + "界"}}); !errors.As(err, &validationErr) {
		t.Fatalf("oversized raw value error=%v want CompanyScopeValidationError", err)
	}
	// 条数不再设上限,只校验单条长度。
	if err := ValidateCompanyScopeInputBounds(make([]ScopeInput, 1000)); err != nil {
		t.Fatalf("rule count should be unbounded, got %v", err)
	}

	d, _, companies := testSetup(t)
	defer d.Close()
	stamp := time.Now().UnixNano()
	companyID, _, err := companies.UpsertCompany(fmt.Sprintf("Scope Limits %d", stamp), "")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupCompany(d, companyID)
	// 曾经封顶 256 条,逐个 IP / 域名录范围的企业很容易撞上;现在不限条数。
	const bulk = 300
	rules := make([]ScopeInput, bulk)
	for i := range rules {
		rules[i] = ScopeInput{Kind: "keyword", Value: fmt.Sprintf("limit-%d-%d", stamp, i)}
	}
	if added, _, _, _, err := companies.AddScopeInputsChecked(companyID, rules, "test"); err != nil || added != bulk {
		t.Fatalf("bulk scope insert: added=%d want=%d err=%v", added, bulk, err)
	}
	if added, _, _, _, err := companies.AddScopeInputsChecked(companyID, []ScopeInput{
		{Kind: "keyword", Value: fmt.Sprintf("limit-%d-extra", stamp)},
	}, "test"); added != 1 || err != nil {
		t.Fatalf("append past the old cap: added=%d err=%v", added, err)
	}
	var count int
	if err := d.QueryRow(`SELECT count(*) FROM company_scope WHERE company_id=$1`, companyID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != bulk+1 {
		t.Fatalf("scope count=%d want=%d", count, bulk+1)
	}

	missingID := companyID + 1_000_000_000
	if _, _, _, _, err := companies.AddScopeInputsChecked(missingID, nil, "test"); !errors.Is(err, ErrCompanyNotFound) {
		t.Fatalf("missing add error=%v want ErrCompanyNotFound", err)
	}
	if _, _, _, err := companies.UpdateScopeInputsChecked(missingID, nil, "test"); !errors.Is(err, ErrCompanyNotFound) {
		t.Fatalf("missing update error=%v want ErrCompanyNotFound", err)
	}
	if _, err := companies.DeleteCompanyWithAssets(missingID, true); !errors.Is(err, ErrCompanyNotFound) {
		t.Fatalf("missing delete error=%v want ErrCompanyNotFound", err)
	}
}

func TestCompanyScopeCheckedReturnsSystemErrorSeparately(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	companies := d.Companies()
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	_, _, _, validationErrors, err := companies.AddScopeInputsChecked(1, nil, "test")
	if err == nil {
		t.Fatal("closed database did not return a system error")
	}
	if len(validationErrors) != 0 {
		t.Fatalf("system error leaked into validation errors: %v", validationErrors)
	}
	var validationErr *CompanyScopeValidationError
	if errors.As(err, &validationErr) || errors.Is(err, ErrCompanyNotFound) {
		t.Fatalf("system error misclassified: %v", err)
	}
}
