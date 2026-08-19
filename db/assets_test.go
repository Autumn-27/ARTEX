package db

import (
	"fmt"
	"slices"
	"testing"
)

// testSetup opens a DB and returns both stores. Skips if no PG.
func testSetup(t *testing.T) (*DB, *AssetStore, *CompanyStore) {
	t.Helper()
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	return d, d.Assets(), d.Companies()
}

// deleteAsset removes one v2 asset by id.
func deleteAsset(d *DB, id int64) {
	d.Exec(`DELETE FROM assets WHERE id = $1`, id)
}

// =====================================================================
// RootDomain
// =====================================================================

func TestUpsertRootDomain(t *testing.T) {
	d, av2, _ := testSetup(t)
	defer d.Close()

	id1, err := av2.UpsertRootDomain(UpsertRootDomainReq{Domain: "roottest.io", ICP: "A12345", TaskID: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer deleteAsset(d, id1)
	if id1 == 0 {
		t.Fatal("expected non-zero id")
	}

	// upsert again — same id (dedup), ICP preserved
	id2, err := av2.UpsertRootDomain(UpsertRootDomainReq{Domain: "roottest.io", TaskID: 2})
	if err != nil {
		t.Fatal(err)
	}
	if id2 != id1 {
		t.Errorf("dedup failed: %d != %d", id2, id1)
	}

	// task_ids should now contain both 1 and 2
	var taskIDs []byte
	d.QueryRow(`SELECT task_ids FROM assets WHERE id = $1`, id1).Scan(&taskIDs)

	// ICP should still be set (COALESCE keeps existing)
	var icp *string
	d.QueryRow(`SELECT icp FROM assets WHERE id = $1`, id1).Scan(&icp)
	if icp == nil || *icp != "A12345" {
		t.Errorf("ICP not preserved: %v", icp)
	}
}

func TestUpsertRootDomainEmpty(t *testing.T) {
	d, av2, _ := testSetup(t)
	defer d.Close()

	_, err := av2.UpsertRootDomain(UpsertRootDomainReq{Domain: ""})
	if err == nil {
		t.Error("expected error for empty domain")
	}
}

// =====================================================================
// IP
// =====================================================================

func TestUpsertIP(t *testing.T) {
	d, av2, _ := testSetup(t)
	defer d.Close()

	id1, err := av2.UpsertIP(UpsertIPReq{
		IP:        "192.168.10.5",
		OpenPorts: []PortService{{Port: 22, Service: "ssh"}, {Port: 80, Service: "http"}},
		TaskID:    1,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer deleteAsset(d, id1)

	// c_segment should be 192.168.10.0/24
	var cseg *string
	d.QueryRow(`SELECT c_segment::text FROM assets WHERE id = $1`, id1).Scan(&cseg)
	if cseg == nil || *cseg != "192.168.10.0/24" {
		t.Errorf("c_segment: want 192.168.10.0/24, got %v", cseg)
	}

	// append another port via UpsertIP (merge)
	id2, err := av2.UpsertIP(UpsertIPReq{
		IP:        "192.168.10.5",
		OpenPorts: []PortService{{Port: 443, Service: "https"}},
		TaskID:    2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if id2 != id1 {
		t.Errorf("dedup failed: %d != %d", id2, id1)
	}

	// verify all 3 ports present
	var cnt int
	d.QueryRow(`SELECT cardinality(open_ports) FROM assets WHERE id = $1`, id1).Scan(&cnt)
	if cnt != 3 {
		t.Errorf("open_ports merge: want 3 ports, got %d", cnt)
	}
}

func TestUpsertIPBoundDomains(t *testing.T) {
	d, av2, _ := testSetup(t)
	defer d.Close()

	id, err := av2.UpsertIP(UpsertIPReq{
		IP:           "10.1.2.3",
		BoundDomains: []string{"a.example.com"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer deleteAsset(d, id)

	// append a second domain
	id2, err := av2.UpsertIP(UpsertIPReq{
		IP:           "10.1.2.3",
		BoundDomains: []string{"b.example.com"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if id2 != id {
		t.Errorf("dedup failed: %d != %d", id2, id)
	}

	var cnt int
	d.QueryRow(`SELECT array_length(bound_domains, 1) FROM assets WHERE id = $1`, id).Scan(&cnt)
	if cnt != 2 {
		t.Errorf("bound_domains merge: want 2, got %d", cnt)
	}
}

func TestAppendIPPort(t *testing.T) {
	d, av2, _ := testSetup(t)
	defer d.Close()

	id, err := av2.UpsertIP(UpsertIPReq{IP: "10.9.8.7"})
	if err != nil {
		t.Fatal(err)
	}
	defer deleteAsset(d, id)

	if err := av2.AppendIPPort("10.9.8.7", 3306, "mysql"); err != nil {
		t.Fatal(err)
	}
	if err := av2.AppendIPPort("10.9.8.7", 22, "ssh"); err != nil {
		t.Fatal(err)
	}

	var cnt int
	d.QueryRow(`SELECT cardinality(open_ports) FROM assets WHERE id = $1`, id).Scan(&cnt)
	if cnt != 2 {
		t.Errorf("AppendIPPort: want 2 ports, got %d", cnt)
	}
}

// =====================================================================
// Subdomain
// =====================================================================

func TestUpsertSubdomain(t *testing.T) {
	d, av2, _ := testSetup(t)
	defer d.Close()

	id, err := av2.UpsertSubdomain(UpsertSubdomainReq{
		Domain:      "sub.subdtest.com",
		RecordType:  "A",
		RecordValue: []string{"1.2.3.4"},
		ICP:         "B99",
		TaskID:      1,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Exec(`DELETE FROM assets WHERE domain IN ('sub.subdtest.com', 'subdtest.com') OR ip = '1.2.3.4'`)

	if id == 0 {
		t.Fatal("expected non-zero id")
	}

	// verify root_domain auto-populated
	var rootDomain string
	d.QueryRow(`SELECT COALESCE(root_domain,'') FROM assets WHERE id = $1`, id).Scan(&rootDomain)
	if rootDomain != "subdtest.com" {
		t.Errorf("root_domain: want subdtest.com, got %q", rootDomain)
	}

	// side effect: root domain asset should exist
	var rootCnt int
	d.QueryRow(`SELECT COUNT(*) FROM assets WHERE type = 'root_domain' AND domain = 'subdtest.com'`).Scan(&rootCnt)
	if rootCnt != 1 {
		t.Error("side-effect root domain not created")
	}

	// side effect: IP asset with bound_domain
	var ipCnt int
	d.QueryRow(`SELECT COUNT(*) FROM assets WHERE type = 'ip' AND ip = '1.2.3.4'`).Scan(&ipCnt)
	if ipCnt != 1 {
		t.Error("side-effect IP asset not created")
	}
}

func TestUpsertSubdomainDedup(t *testing.T) {
	d, av2, _ := testSetup(t)
	defer d.Close()

	id1, err := av2.UpsertSubdomain(UpsertSubdomainReq{Domain: "dedup.subdtest2.net", RecordType: "A", RecordValue: []string{"5.5.5.5"}})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Exec(`DELETE FROM assets WHERE domain IN ('dedup.subdtest2.net', 'subdtest2.net') OR ip = '5.5.5.5'`)

	id2, err := av2.UpsertSubdomain(UpsertSubdomainReq{Domain: "dedup.subdtest2.net", RecordType: "A", RecordValue: []string{"5.5.5.5"}})
	if err != nil {
		t.Fatal(err)
	}
	if id2 != id1 {
		t.Errorf("subdomain dedup failed: %d vs %d", id2, id1)
	}
}

// =====================================================================
// App
// =====================================================================

func TestUpsertAppByBundle(t *testing.T) {
	d, av2, _ := testSetup(t)
	defer d.Close()

	id1, err := av2.UpsertApp(UpsertAppReq{Name: "MyApp", BundleID: "com.example.myapp", Category: "tools"})
	if err != nil {
		t.Fatal(err)
	}
	defer deleteAsset(d, id1)

	// dedup by bundle
	id2, err := av2.UpsertApp(UpsertAppReq{Name: "MyApp Updated", BundleID: "com.example.myapp"})
	if err != nil {
		t.Fatal(err)
	}
	if id2 != id1 {
		t.Errorf("app bundle dedup failed: %d vs %d", id2, id1)
	}
}

func TestUpsertAppByName(t *testing.T) {
	d, av2, _ := testSetup(t)
	defer d.Close()

	id1, err := av2.UpsertApp(UpsertAppReq{Name: "UniqueAppNoBundle", Category: "utility"})
	if err != nil {
		t.Fatal(err)
	}
	defer deleteAsset(d, id1)

	id2, err := av2.UpsertApp(UpsertAppReq{Name: "UniqueAppNoBundle"})
	if err != nil {
		t.Fatal(err)
	}
	if id2 != id1 {
		t.Errorf("app name dedup failed: %d vs %d", id2, id1)
	}
}

// =====================================================================
// HTTPService
// =====================================================================

func TestUpsertHTTPService(t *testing.T) {
	d, av2, _ := testSetup(t)
	defer d.Close()

	sc := 200
	cl := int64(1024)
	id1, err := av2.UpsertHTTPService(UpsertHTTPServiceReq{
		URL:          "https://www.httptest.example.com/",
		Technologies: []string{"nginx", "vue"},
		StatusCode:   &sc,
		ContentLength: &cl,
		PageTitle:    "Test Site",
		TaskID:       1,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Exec(`DELETE FROM assets WHERE url = 'https://www.httptest.example.com' OR domain = 'www.httptest.example.com' OR domain = 'httptest.example.com'`)

	if id1 == 0 {
		t.Fatal("expected non-zero id")
	}

	// verify technology was stored
	var techCnt int
	d.QueryRow(`SELECT array_length(technologies, 1) FROM assets WHERE id = $1`, id1).Scan(&techCnt)
	if techCnt != 2 {
		t.Errorf("technologies: want 2, got %d", techCnt)
	}

	// upsert again with additional tech → should merge (append)
	id2, err := av2.UpsertHTTPService(UpsertHTTPServiceReq{
		URL:          "https://www.httptest.example.com/",
		Technologies: []string{"react", "webpack"},
		TaskID:       2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if id2 != id1 {
		t.Errorf("http service dedup failed: %d vs %d", id2, id1)
	}

	d.QueryRow(`SELECT array_length(technologies, 1) FROM assets WHERE id = $1`, id1).Scan(&techCnt)
	if techCnt != 4 {
		t.Errorf("technologies merge: want 4, got %d", techCnt)
	}
}

func TestUpsertHTTPServiceAuthAppend(t *testing.T) {
	d, av2, _ := testSetup(t)
	defer d.Close()

	auth1 := []map[string]any{{"type": "basic", "username": "admin", "password": "pass"}}
	id1, err := av2.UpsertHTTPService(UpsertHTTPServiceReq{
		URL:    "http://authtest.example.org/",
		Auth:   auth1,
		TaskID: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Exec(`DELETE FROM assets WHERE url = 'http://authtest.example.org' OR domain IN ('authtest.example.org', 'example.org')`)

	auth2 := []map[string]any{{"type": "bearer", "token": "tok123"}}
	id2, err := av2.UpsertHTTPService(UpsertHTTPServiceReq{
		URL:    "http://authtest.example.org/",
		Auth:   auth2,
		TaskID: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if id2 != id1 {
		t.Errorf("dedup failed: %d vs %d", id2, id1)
	}

	// auth should have 2 items now
	var authCnt int
	d.QueryRow(`SELECT cardinality(auth) FROM assets WHERE id = $1`, id1).Scan(&authCnt)
	if authCnt != 2 {
		t.Errorf("auth append: want 2, got %d", authCnt)
	}
}

// =====================================================================
// OtherService
// =====================================================================

func TestUpsertOtherService(t *testing.T) {
	d, av2, _ := testSetup(t)
	defer d.Close()

	id1, err := av2.UpsertOtherService(UpsertOtherServiceReq{
		IP:          "172.16.0.1",
		Port:        22,
		ServiceName: "ssh",
		TaskID:      1,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Exec(`DELETE FROM assets WHERE (type = 'service' AND service_name = 'ssh' AND ip = '172.16.0.1') OR (type = 'ip' AND ip = '172.16.0.1')`)

	if id1 == 0 {
		t.Fatal("expected non-zero id")
	}

	// dedup
	id2, err := av2.UpsertOtherService(UpsertOtherServiceReq{
		IP:          "172.16.0.1",
		Port:        22,
		ServiceName: "ssh",
	})
	if err != nil {
		t.Fatal(err)
	}
	if id2 != id1 {
		t.Errorf("other service dedup failed: %d vs %d", id2, id1)
	}

	// side-effect: IP asset should have port 22 in open_ports
	var cnt int
	d.QueryRow(`SELECT cardinality(open_ports) FROM assets WHERE type='ip' AND ip='172.16.0.1'`).Scan(&cnt)
	if cnt == 0 {
		t.Error("side-effect: IP open_ports not populated")
	}
}

func TestUpsertOtherServiceAuthAppend(t *testing.T) {
	d, av2, _ := testSetup(t)
	defer d.Close()

	auth1 := []map[string]any{{"username": "admin", "password": "secret"}}
	id1, err := av2.UpsertOtherService(UpsertOtherServiceReq{
		IP:          "10.5.5.5",
		Port:        3306,
		ServiceName: "mysql",
		Auth:        auth1,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Exec(`DELETE FROM assets WHERE (type = 'service' AND ip = '10.5.5.5') OR (type = 'ip' AND ip = '10.5.5.5')`)

	auth2 := []map[string]any{{"username": "root", "password": "root"}}
	id2, err := av2.UpsertOtherService(UpsertOtherServiceReq{
		IP:          "10.5.5.5",
		Port:        3306,
		ServiceName: "mysql",
		Auth:        auth2,
	})
	if err != nil || id2 != id1 {
		t.Fatalf("dedup or error: %v, ids %d vs %d", err, id2, id1)
	}

	var authCnt int
	d.QueryRow(`SELECT cardinality(auth) FROM assets WHERE id = $1`, id1).Scan(&authCnt)
	if authCnt != 2 {
		t.Errorf("auth append: want 2, got %d", authCnt)
	}
}

// =====================================================================
// Endpoint
// =====================================================================

func TestUpsertEndpoint(t *testing.T) {
	d, av2, _ := testSetup(t)
	defer d.Close()

	params1 := []map[string]any{{"location": "query", "name": "id", "value": "1"}}
	id1, err := av2.UpsertEndpoint(UpsertEndpointReq{
		URL:    "https://api.eptest.com/users?id=1",
		Method: "GET",
		Params: params1,
		TaskID: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Exec(`DELETE FROM assets WHERE url LIKE '%eptest.com%' OR domain IN ('api.eptest.com', 'eptest.com')`)

	if id1 == 0 {
		t.Fatal("expected non-zero id")
	}

	// dedup (same URL + method = same endpoint)
	id2, err := av2.UpsertEndpoint(UpsertEndpointReq{
		URL:    "https://api.eptest.com/users?id=1",
		Method: "GET",
		Params: []map[string]any{{"location": "query", "name": "filter", "value": "active"}},
		TaskID: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if id2 != id1 {
		t.Errorf("endpoint dedup failed: %d vs %d", id2, id1)
	}

	// params should be merged (2 distinct params)
	var paramCnt int
	d.QueryRow(`SELECT cardinality(params) FROM assets WHERE id = $1`, id1).Scan(&paramCnt)
	if paramCnt != 2 {
		t.Errorf("params merge: want 2, got %d", paramCnt)
	}
}

func TestUpsertEndpointRequiredFields(t *testing.T) {
	d, av2, _ := testSetup(t)
	defer d.Close()

	_, err := av2.UpsertEndpoint(UpsertEndpointReq{URL: "", Method: "GET"})
	if err == nil {
		t.Error("expected error for empty URL")
	}

	_, err = av2.UpsertEndpoint(UpsertEndpointReq{URL: "https://example.com/", Method: ""})
	if err == nil {
		t.Error("expected error for empty method")
	}
}

// =====================================================================
// Query helpers
// =====================================================================

func TestQueryByType(t *testing.T) {
	d, av2, _ := testSetup(t)
	defer d.Close()

	id, err := av2.UpsertRootDomain(UpsertRootDomainReq{Domain: "querytest.example.net"})
	if err != nil {
		t.Fatal(err)
	}
	defer deleteAsset(d, id)

	assets, err := av2.QueryByType("root_domain", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, a := range assets {
		if a.ID == id {
			found = true
		}
	}
	if !found {
		t.Error("QueryByType: inserted asset not found")
	}
}

// TestDeleteByTaskID: 独有资产被删,与其他任务共享的资产仅解除关联(保留),host 反查正确。
func TestDeleteByTaskID(t *testing.T) {
	d, av2, _ := testSetup(t)
	defer d.Close()

	const taskA = int64(90001)
	const taskB = int64(90002)
	// solo:仅属 taskA
	solo, err := av2.UpsertRootDomain(UpsertRootDomainReq{Domain: "solo-del.test", TaskID: taskA})
	if err != nil {
		t.Fatal(err)
	}
	defer deleteAsset(d, solo)
	// shared:先 taskA 再 taskB → task_ids={A,B}
	shared, err := av2.UpsertRootDomain(UpsertRootDomainReq{Domain: "shared-del.test", TaskID: taskA})
	if err != nil {
		t.Fatal(err)
	}
	defer deleteAsset(d, shared)
	if _, err := av2.UpsertRootDomain(UpsertRootDomainReq{Domain: "shared-del.test", TaskID: taskB}); err != nil {
		t.Fatal(err)
	}

	// host 反查(删资产前):应含两个域名
	hosts, err := av2.HostsByTask(taskA)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(hosts, "solo-del.test") || !slices.Contains(hosts, "shared-del.test") {
		t.Fatalf("HostsByTask 缺 host: %v", hosts)
	}

	n, err := av2.DeleteByTaskID(taskA)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("DeleteByTaskID: 应删 1 个独有资产,实删 %d", n)
	}
	// solo 已删
	if a, _ := av2.GetByIDs([]int64{solo}); len(a) != 0 {
		t.Fatalf("solo 资产应被删除")
	}
	// shared 保留,且 task_ids 只剩 taskB
	sa, _ := av2.GetByIDs([]int64{shared})
	if len(sa) != 1 {
		t.Fatalf("shared 资产应保留")
	}
	if slices.Contains(sa[0].TaskIDs, taskA) || !slices.Contains(sa[0].TaskIDs, taskB) {
		t.Fatalf("shared task_ids 应解除 A 保留 B,得 %v", sa[0].TaskIDs)
	}
}

func TestQueryByTask(t *testing.T) {
	d, av2, _ := testSetup(t)
	defer d.Close()

	const taskID = int64(99999)
	id, err := av2.UpsertRootDomain(UpsertRootDomainReq{Domain: "taskquery.net", TaskID: taskID})
	if err != nil {
		t.Fatal(err)
	}
	defer deleteAsset(d, id)

	assets, err := av2.QueryByTask(taskID, "", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, a := range assets {
		if a.ID == id {
			found = true
		}
	}
	if !found {
		t.Error("QueryByTask: inserted asset not found")
	}

	n, err := av2.CountByTask(taskID, "")
	if err != nil {
		t.Fatal(err)
	}
	if n < 1 {
		t.Errorf("CountByTask: got %d, want >= 1", n)
	}
	if counts, err := av2.CountsByTypeForTask(taskID); err != nil {
		t.Fatal(err)
	} else if counts["root_domain"] < 1 {
		t.Errorf("CountsByTypeForTask: root_domain = %d, want >= 1", counts["root_domain"])
	}

	// offset past the end must return nothing, not fall back to page 1
	rest, err := av2.QueryByTask(taskID, "", 10, n)
	if err != nil {
		t.Fatal(err)
	}
	if len(rest) != 0 {
		t.Errorf("QueryByTask offset=%d: got %d rows, want 0", n, len(rest))
	}
}

// 任务资产列表按页取,不再被固定条数截断:60 条资产用 25/页要能完整翻出来。
func TestQueryByTaskPaging(t *testing.T) {
	d, av2, _ := testSetup(t)
	defer d.Close()

	const taskID = int64(99998)
	const n = 60
	for i := 0; i < n; i++ {
		id, err := av2.UpsertRootDomain(UpsertRootDomainReq{
			Domain: fmt.Sprintf("paging-%02d.querybytask.test", i),
			TaskID: taskID,
		})
		if err != nil {
			t.Fatal(err)
		}
		defer deleteAsset(d, id)
	}

	total, err := av2.CountByTask(taskID, "root_domain")
	if err != nil {
		t.Fatal(err)
	}
	if total != n {
		t.Fatalf("CountByTask: got %d, want %d", total, n)
	}

	const size = 25
	seen := map[int64]bool{}
	for offset := 0; offset < total; offset += size {
		page, err := av2.QueryByTask(taskID, "root_domain", size, offset)
		if err != nil {
			t.Fatal(err)
		}
		for _, a := range page {
			if seen[a.ID] {
				t.Errorf("offset=%d: asset %d returned twice", offset, a.ID)
			}
			seen[a.ID] = true
		}
	}
	if len(seen) != n {
		t.Errorf("paged through %d assets, want %d", len(seen), n)
	}
}

func TestQueryByCompany(t *testing.T) {
	d, av2, cs := testSetup(t)
	defer d.Close()

	companyID, _, err := cs.UpsertCompany("QueryByCompanyCorp", "")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupCompany(d, companyID)

	cs.AddScope(companyID, []string{"qbc-test.io"}, "test")

	id, err := av2.UpsertRootDomain(UpsertRootDomainReq{Domain: "qbc-test.io"})
	if err != nil {
		t.Fatal(err)
	}
	defer deleteAsset(d, id)

	if err := cs.RecomputeAttribution(); err != nil {
		t.Fatal(err)
	}

	assets, err := av2.QueryByCompany(companyID, "root_domain", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, a := range assets {
		if a.ID == id {
			found = true
		}
	}
	if !found {
		t.Error("QueryByCompany: attributed asset not found")
	}

	n, err := av2.CountByCompany(companyID, "root_domain")
	if err != nil {
		t.Fatal(err)
	}
	if n < 1 {
		t.Errorf("CountByCompany: got %d, want >= 1", n)
	}

	// offset past the end must return nothing, not fall back to page 1
	rest, err := av2.QueryByCompany(companyID, "root_domain", 10, n)
	if err != nil {
		t.Fatal(err)
	}
	if len(rest) != 0 {
		t.Errorf("QueryByCompany offset=%d: got %d rows, want 0", n, len(rest))
	}
}

// 企业资产列表同样按页取,不被固定条数截断。
func TestQueryByCompanyPaging(t *testing.T) {
	d, av2, cs := testSetup(t)
	defer d.Close()

	companyID, _, err := cs.UpsertCompany("QueryByCompanyPagingCorp", "")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupCompany(d, companyID)

	if added, _, _, errs := cs.AddScope(companyID, []string{"qbc-paging.io"}, "test"); added != 1 {
		t.Fatalf("AddScope: added=%d, errors=%v", added, errs)
	}

	// UpsertSubdomain 会顺带建根域名资产,一并清掉
	defer d.Exec(`DELETE FROM assets WHERE root_domain = 'qbc-paging.io'`)

	const n = 60
	for i := 0; i < n; i++ {
		if _, err := av2.UpsertSubdomain(UpsertSubdomainReq{
			Domain: fmt.Sprintf("paging-%02d.qbc-paging.io", i),
		}); err != nil {
			t.Fatal(err)
		}
	}

	total, err := av2.CountByCompany(companyID, "subdomain")
	if err != nil {
		t.Fatal(err)
	}
	if total != n {
		t.Fatalf("CountByCompany: got %d, want %d", total, n)
	}

	const size = 25
	seen := map[int64]bool{}
	for offset := 0; offset < total; offset += size {
		page, err := av2.QueryByCompany(companyID, "subdomain", size, offset)
		if err != nil {
			t.Fatal(err)
		}
		for _, a := range page {
			if seen[a.ID] {
				t.Errorf("offset=%d: asset %d returned twice", offset, a.ID)
			}
			seen[a.ID] = true
		}
	}
	if len(seen) != n {
		t.Errorf("paged through %d assets, want %d", len(seen), n)
	}
}

func TestCountsByType(t *testing.T) {
	d, av2, _ := testSetup(t)
	defer d.Close()

	id, err := av2.UpsertRootDomain(UpsertRootDomainReq{Domain: "counttest.example.biz"})
	if err != nil {
		t.Fatal(err)
	}
	defer deleteAsset(d, id)

	counts, err := av2.CountsByType()
	if err != nil {
		t.Fatal(err)
	}
	if counts["root_domain"] == 0 {
		t.Error("CountsByType: root_domain count should be > 0")
	}
}

// =====================================================================
// Company attribution at insert time
// =====================================================================

func TestCompanyAttributionAtInsertTime(t *testing.T) {
	d, av2, cs := testSetup(t)
	defer d.Close()

	companyID, _, err := cs.UpsertCompany("InsertAttrCorp", "")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupCompany(d, companyID)

	cs.AddScope(companyID, []string{"insertattr.com"}, "test")

	// Now insert a root domain — should be auto-attributed
	assetID, err := av2.UpsertRootDomain(UpsertRootDomainReq{Domain: "insertattr.com"})
	if err != nil {
		t.Fatal(err)
	}
	defer deleteAsset(d, assetID)

	var cid *int64
	d.QueryRow(`SELECT company_id FROM assets WHERE id = $1`, assetID).Scan(&cid)
	if cid == nil || *cid != companyID {
		t.Errorf("attribution at insert time: want company %d, got %v", companyID, cid)
	}
}

// =====================================================================
// calcCSegment
// =====================================================================

func TestCalcCSegment(t *testing.T) {
	tests := []struct {
		ip   string
		want string
	}{
		{"192.168.1.5", "192.168.1.0/24"},
		{"10.0.0.255", "10.0.0.0/24"},
		{"", ""},
		{"notanip", ""},
	}
	for _, tc := range tests {
		got := calcCSegment(tc.ip)
		if got != tc.want {
			t.Errorf("calcCSegment(%q): want %q, got %q", tc.ip, tc.want, got)
		}
	}
}

// =====================================================================
// marshalStringArray
// =====================================================================

func TestMarshalStringArray(t *testing.T) {
	got := marshalStringArray([]string{"a", "b", "c"})
	if got != `{"a","b","c"}` {
		t.Errorf("unexpected: %q", got)
	}
	got2 := marshalStringArray(nil)
	if got2 != "{}" {
		t.Errorf("empty: %q", got2)
	}
}

// =====================================================================
// normalizeURL
// =====================================================================

func TestNormalizeURL(t *testing.T) {
	tests := []struct {
		raw  string
		want string
	}{
		{"HTTPS://Example.COM/path", "https://example.com/path"},
		{"http://example.com/", "http://example.com"},
		{"http://example.com/page", "http://example.com/page"},
	}
	for _, tc := range tests {
		got := normalizeURL(tc.raw)
		if got != tc.want {
			t.Errorf("normalizeURL(%q): want %q, got %q", tc.raw, tc.want, got)
		}
	}
}
