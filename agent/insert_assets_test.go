package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Autumn-27/artex/db"
)

// testDB opens a DB connection, skipping if PG is unavailable.
func testDB(t *testing.T) *db.DB {
	t.Helper()
	dsn, _, err := db.DSN()
	if err != nil {
		t.Skipf("no database config (%v)", err)
	}
	d, err := db.Open(dsn)
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	return d
}

// callInsertAssets calls the insert_assets tool with the given payload.
func callInsertAssets(t *testing.T, ts *ToolSet, payload any) map[string]any {
	t.Helper()
	raw, _ := json.Marshal(payload)
	tool := ts.insertAssets()
	res, err := tool.Call(context.Background(), raw, nil)
	if err != nil {
		t.Fatalf("insertAssets Call error: %v", err)
	}
	text := res.Flatten()
	var out map[string]any
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatalf("unmarshal result: %v\nraw: %s", err, text)
	}
	return out
}

// =====================================================================
// TestInsertAssetsSubdomainSideEffects
// 子域名插入 → 自动创建 root_domain + IP 资产，IP 绑定域名
// =====================================================================
func TestInsertAssetsSubdomainSideEffects(t *testing.T) {
	d := testDB(t)
	defer d.Close()

	ts := NewToolSet(nil, "")
	ts.SetAssetStore(d.Assets(), d.Companies())
	defer d.Exec(`DELETE FROM assets WHERE domain IN ('ia-sub.sideeffect-test.com','sideeffect-test.com') OR ip='7.8.9.10'`)

	out := callInsertAssets(t, ts, map[string]any{
		"assets": []any{
			map[string]any{
				"type":         "subdomain",
				"domain":       "ia-sub.sideeffect-test.com",
				"record_type":  "A",
				"record_value": []string{"7.8.9.10"},
			},
		},
		"task_id": 999,
	})

	// no errors
	if errs, _ := out["errors"].([]any); len(errs) > 0 {
		t.Errorf("unexpected errors: %v", errs)
	}
	results, _ := out["results"].([]any)
	if len(results) == 0 {
		t.Fatal("no results returned")
	}

	// root_domain should exist
	var rootCnt int
	d.QueryRow(`SELECT COUNT(*) FROM assets WHERE type='root_domain' AND domain='sideeffect-test.com'`).Scan(&rootCnt)
	if rootCnt != 1 {
		t.Errorf("side-effect: root_domain not created, got %d", rootCnt)
	}

	// IP asset should exist with bound_domains containing our subdomain
	var ipID int64
	var boundDomains []byte
	d.QueryRow(`SELECT id, array_to_json(bound_domains)::text FROM assets WHERE type='ip' AND ip='7.8.9.10'`).Scan(&ipID, &boundDomains)
	if ipID == 0 {
		t.Error("side-effect: IP asset not created")
	}
	var domains []string
	json.Unmarshal(boundDomains, &domains)
	found := false
	for _, d := range domains {
		if d == "ia-sub.sideeffect-test.com" {
			found = true
		}
	}
	if !found {
		t.Errorf("side-effect: bound_domains should contain subdomain, got %v", domains)
	}

	// record_value stored as array
	var rvRaw []byte
	d.QueryRow(`SELECT array_to_json(record_value)::text FROM assets WHERE type='subdomain' AND domain='ia-sub.sideeffect-test.com'`).Scan(&rvRaw)
	var rv []string
	json.Unmarshal(rvRaw, &rv)
	if len(rv) == 0 || rv[0] != "7.8.9.10" {
		t.Errorf("record_value stored incorrectly: %v", rv)
	}
}

// =====================================================================
// TestInsertAssetsMultiIPSubdomain
// 多个 IP 的子域名：所有 IP 都应存入 record_value[]，各自创建 IP 资产
// =====================================================================
func TestInsertAssetsMultiIPSubdomain(t *testing.T) {
	d := testDB(t)
	defer d.Close()

	ts := NewToolSet(nil, "")
	ts.SetAssetStore(d.Assets(), d.Companies())
	defer d.Exec(`DELETE FROM assets WHERE domain IN ('multi.multiip-test.io','multiip-test.io') OR ip IN ('1.1.1.1','2.2.2.2')`)

	out := callInsertAssets(t, ts, map[string]any{
		"assets": []any{
			map[string]any{
				"type":         "subdomain",
				"domain":       "multi.multiip-test.io",
				"record_type":  "A",
				"record_value": []string{"1.1.1.1", "2.2.2.2"},
			},
		},
	})

	if errs, _ := out["errors"].([]any); len(errs) > 0 {
		t.Errorf("unexpected errors: %v", errs)
	}

	// Both IPs should have IP assets
	var ip1Cnt, ip2Cnt int
	d.QueryRow(`SELECT COUNT(*) FROM assets WHERE type='ip' AND ip='1.1.1.1'`).Scan(&ip1Cnt)
	d.QueryRow(`SELECT COUNT(*) FROM assets WHERE type='ip' AND ip='2.2.2.2'`).Scan(&ip2Cnt)
	if ip1Cnt != 1 {
		t.Error("IP 1.1.1.1 asset not created")
	}
	if ip2Cnt != 1 {
		t.Error("IP 2.2.2.2 asset not created")
	}

	// record_value should contain both IPs
	var rvRaw []byte
	d.QueryRow(`SELECT array_to_json(record_value)::text FROM assets WHERE type='subdomain' AND domain='multi.multiip-test.io'`).Scan(&rvRaw)
	var rv []string
	json.Unmarshal(rvRaw, &rv)
	if len(rv) != 2 {
		t.Errorf("record_value: want 2 IPs, got %v", rv)
	}
}

// =====================================================================
// TestInsertAssetsHTTPServiceTechnologies
// HTTP 服务插入：technologies 存储并可读回；IP 存在时域名和端口写入 IP 资产
// =====================================================================
func TestInsertAssetsHTTPServiceTechnologies(t *testing.T) {
	d := testDB(t)
	defer d.Close()

	ts := NewToolSet(nil, "")
	ts.SetAssetStore(d.Assets(), d.Companies())
	defer d.Exec(`DELETE FROM assets WHERE url='https://tech-test.example.com' OR domain IN ('tech-test.example.com','example.com') OR ip='3.4.5.6'`)

	out := callInsertAssets(t, ts, map[string]any{
		"assets": []any{
			map[string]any{
				"type":         "service",
				"url":          "https://tech-test.example.com",
				"service_ip":   "3.4.5.6",
				"technologies": []string{"Nginx", "Vue.js", "Cloudflare"},
				"status_code":  200,
				"page_title":   "Tech Test Site",
			},
		},
		"task_id": 888,
	})

	if errs, _ := out["errors"].([]any); len(errs) > 0 {
		t.Errorf("unexpected errors: %v", errs)
	}

	// technologies should be stored
	var techCnt int
	d.QueryRow(`SELECT array_length(technologies,1) FROM assets WHERE url='https://tech-test.example.com'`).Scan(&techCnt)
	if techCnt != 3 {
		t.Errorf("technologies: want 3, got %d", techCnt)
	}

	// QueryByType should return technologies correctly (verifies array_to_json scan)
	assets, err := d.Assets().QueryByType("service", 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	var found *db.Asset
	for _, a := range assets {
		if a.URL == "https://tech-test.example.com" {
			found = a
			break
		}
	}
	if found == nil {
		t.Fatal("service not found via QueryByType")
	}
	if len(found.Technologies) != 3 {
		t.Errorf("QueryByType: technologies roundtrip failed, got %v", found.Technologies)
	}

	// side effect: IP asset should exist with bound_domains containing the service domain
	var ipID int64
	var bdRaw []byte
	var portCnt int
	d.QueryRow(`SELECT id, array_to_json(bound_domains)::text FROM assets WHERE type='ip' AND ip='3.4.5.6'`).Scan(&ipID, &bdRaw)
	if ipID == 0 {
		t.Error("side-effect: IP asset not created for HTTP service IP")
	}
	var bd []string
	json.Unmarshal(bdRaw, &bd)
	hasDomain := false
	for _, dom := range bd {
		if dom == "tech-test.example.com" {
			hasDomain = true
		}
	}
	if !hasDomain {
		t.Errorf("side-effect: IP bound_domains missing service domain, got %v", bd)
	}

	// side effect: IP open_ports should contain port 443
	d.QueryRow(`SELECT cardinality(open_ports) FROM assets WHERE type='ip' AND ip='3.4.5.6'`).Scan(&portCnt)
	if portCnt == 0 {
		t.Error("side-effect: IP open_ports not set for HTTP service")
	}
}

// =====================================================================
// TestInsertAssetsOtherService
// 非 HTTP 服务：c_segment 自动生成，IP 资产含 open_ports 和 bound_domains
// =====================================================================
func TestInsertAssetsOtherService(t *testing.T) {
	d := testDB(t)
	defer d.Close()

	ts := NewToolSet(nil, "")
	ts.SetAssetStore(d.Assets(), d.Companies())
	defer d.Exec(`DELETE FROM assets WHERE
		(type='service' AND ip='10.20.30.40') OR
		(type='ip' AND ip='10.20.30.40') OR
		domain IN ('db.othersvc-test.com','othersvc-test.com')`)

	out := callInsertAssets(t, ts, map[string]any{
		"assets": []any{
			map[string]any{
				"type":         "service",
				"ip":           "10.20.30.40",
				"domain":       "db.othersvc-test.com",
				"port":         3306,
				"service_name": "mysql",
			},
		},
	})

	if errs, _ := out["errors"].([]any); len(errs) > 0 {
		t.Errorf("unexpected errors: %v", errs)
	}

	// c_segment should be auto-set on the service
	var cseg *string
	d.QueryRow(`SELECT c_segment::text FROM assets WHERE type='service' AND ip='10.20.30.40'`).Scan(&cseg)
	if cseg == nil || *cseg != "10.20.30.0/24" {
		t.Errorf("c_segment: want 10.20.30.0/24, got %v", cseg)
	}

	// IP side-effect: port 3306 in open_ports
	var portCnt int
	d.QueryRow(`SELECT cardinality(open_ports) FROM assets WHERE type='ip' AND ip='10.20.30.40'`).Scan(&portCnt)
	if portCnt == 0 {
		t.Error("side-effect: IP open_ports should contain port 3306")
	}

	// IP side-effect: bound_domains contains the service domain
	var bdRaw []byte
	d.QueryRow(`SELECT array_to_json(bound_domains)::text FROM assets WHERE type='ip' AND ip='10.20.30.40'`).Scan(&bdRaw)
	var bd []string
	json.Unmarshal(bdRaw, &bd)
	hasDomain := false
	for _, dom := range bd {
		if dom == "db.othersvc-test.com" {
			hasDomain = true
		}
	}
	if !hasDomain {
		t.Errorf("side-effect: IP bound_domains missing service domain, got %v", bd)
	}
}

// =====================================================================
// TestInsertAssetsMixedBatch
// 混合批量插入：一次调用插入多种类型
// =====================================================================
func TestInsertAssetsMixedBatch(t *testing.T) {
	d := testDB(t)
	defer d.Close()

	ts := NewToolSet(nil, "")
	ts.SetAssetStore(d.Assets(), d.Companies())
	defer d.Exec(`DELETE FROM assets WHERE
		domain IN ('batch-sub.batch-test.org','batch-test.org') OR
		ip='55.66.77.88' OR
		url='https://batch-test.org/api' OR
		(type='endpoint' AND url='https://batch-test.org/api/users')`)

	out := callInsertAssets(t, ts, map[string]any{
		"assets": []any{
			// root_domain
			map[string]any{"type": "root_domain", "domain": "batch-test.org"},
			// subdomain with A record
			map[string]any{"type": "subdomain", "domain": "batch-sub.batch-test.org", "record_type": "A", "record_value": []string{"55.66.77.88"}},
			// HTTP service
			map[string]any{"type": "service", "url": "https://batch-test.org/api", "technologies": []string{"Go", "PostgreSQL"}, "status_code": 200},
			// endpoint
			map[string]any{"type": "endpoint", "url": "https://batch-test.org/api/users", "method": "GET"},
		},
		"task_id": 777,
	})

	if errs, _ := out["errors"].([]any); len(errs) > 0 {
		t.Errorf("unexpected errors: %v", errs)
	}
	results, _ := out["results"].([]any)
	if len(results) != 4 {
		t.Errorf("mixed batch: want 4 results, got %d", len(results))
	}

	// verify all types exist in DB
	types := []string{"root_domain", "subdomain", "service", "endpoint"}
	for _, typ := range types {
		var cnt int
		switch typ {
		case "root_domain":
			d.QueryRow(`SELECT COUNT(*) FROM assets WHERE type=$1 AND domain='batch-test.org'`, typ).Scan(&cnt)
		case "subdomain":
			d.QueryRow(`SELECT COUNT(*) FROM assets WHERE type=$1 AND domain='batch-sub.batch-test.org'`, typ).Scan(&cnt)
		case "service":
			d.QueryRow(`SELECT COUNT(*) FROM assets WHERE type=$1 AND url='https://batch-test.org/api'`, typ).Scan(&cnt)
		case "endpoint":
			d.QueryRow(`SELECT COUNT(*) FROM assets WHERE type=$1 AND url='https://batch-test.org/api/users'`, typ).Scan(&cnt)
		}
		if cnt != 1 {
			t.Errorf("mixed batch: %s not found in DB", typ)
		}
	}
}

// =====================================================================
// TestInsertAssetsDedup
// 幂等写入：同一资产插入两次，返回相同 ID
// =====================================================================
func TestInsertAssetsDedup(t *testing.T) {
	d := testDB(t)
	defer d.Close()

	ts := NewToolSet(nil, "")
	ts.SetAssetStore(d.Assets(), d.Companies())
	defer d.Exec(`DELETE FROM assets WHERE domain='dedup-ia.deduptest.net' OR domain='deduptest.net'`)

	payload := map[string]any{
		"assets": []any{
			map[string]any{"type": "root_domain", "domain": "deduptest.net"},
		},
	}

	out1 := callInsertAssets(t, ts, payload)
	out2 := callInsertAssets(t, ts, payload)

	getID := func(out map[string]any) float64 {
		results, _ := out["results"].([]any)
		if len(results) == 0 {
			return 0
		}
		m, _ := results[0].(map[string]any)
		id, _ := m["id"].(float64)
		return id
	}

	id1, id2 := getID(out1), getID(out2)
	if id1 == 0 || id1 != id2 {
		t.Errorf("dedup: want same ID on double insert, got %v vs %v", id1, id2)
	}
}
