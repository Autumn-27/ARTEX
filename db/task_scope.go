package db

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

// TaskScope is one row of a task's test scope — the coverage denominator and the
// per-task authorization edge. Rows come either from insertAssets (source='auto',
// conservative, one per explicitly-inserted asset) or from the add_task_scope tool
// (source='agent', for company / whole-root-domain / subdomain / ip).
type TaskScope struct {
	ID        int64  `json:"id"`
	TaskID    int64  `json:"task_id"`
	Kind      string `json:"kind"` // company|root_domain|subdomain|ip|cidr
	CompanyID *int64 `json:"company_id,omitempty"`
	Domain    string `json:"domain,omitempty"`
	Net       string `json:"net,omitempty"`
	Source    string `json:"source"`
	Reason    string `json:"reason,omitempty"`
}

// ipToHostCIDR turns a bare IP into its single-host CIDR (/32 or /128). "" if invalid.
func ipToHostCIDR(ip string) string {
	ip = strings.TrimSpace(ip)
	p := net.ParseIP(ip)
	if p == nil {
		return ""
	}
	if p.To4() != nil {
		return ip + "/32"
	}
	return ip + "/128"
}

// upsertTaskScope inserts one scope row idempotently (uq_task_scope). A duplicate is
// silently ignored. taskID<=0 or empty kind → no-op.
func (s *AssetStore) upsertTaskScope(ts TaskScope) error {
	if ts.TaskID <= 0 || ts.Kind == "" {
		return nil
	}
	var domainVal, netVal, companyVal any
	if ts.Domain != "" {
		domainVal = ts.Domain
	}
	if ts.Net != "" {
		netVal = ts.Net
	}
	if ts.CompanyID != nil && *ts.CompanyID > 0 {
		companyVal = *ts.CompanyID
	}
	src := ts.Source
	if src == "" {
		src = "auto"
	}
	_, err := s.db.Exec(`
INSERT INTO task_scope(task_id, kind, company_id, domain, net, source, reason)
VALUES ($1,$2,$3,$4,$5::cidr,$6,NULLIF($7,''))
ON CONFLICT DO NOTHING`, ts.TaskID, ts.Kind, companyVal, domainVal, netVal, src, ts.Reason)
	return err
}

// AddAutoScope records the conservative task scope implied by ONE explicitly-inserted
// asset item (source='auto'). MUST be called only from insertAssets' top-level loop —
// never from a db-layer side effect (linkHostAssets), so派生资产不会盲目扩大范围。
// Rule: scope granularity follows the asset's own type. taskID<=0 → no-op.
func (s *AssetStore) AddAutoScope(taskID int64, assetType, domain, rawURL, ip string) error {
	if taskID <= 0 {
		return nil
	}
	switch assetType {
	case "root_domain":
		if d := DomainKey(domain); d != "" {
			return s.upsertTaskScope(TaskScope{TaskID: taskID, Kind: "root_domain", Domain: d})
		}
	case "subdomain":
		if d := DomainKey(domain); d != "" {
			return s.upsertTaskScope(TaskScope{TaskID: taskID, Kind: "subdomain", Domain: d})
		}
	case "service", "endpoint":
		host := domain
		if host == "" && rawURL != "" {
			host, _, _ = parseURL(normalizeURL(rawURL))
		}
		host = DomainKey(host)
		if host != "" && net.ParseIP(host) == nil {
			return s.upsertTaskScope(TaskScope{TaskID: taskID, Kind: "subdomain", Domain: host})
		}
		// IP-literal host or no host → fall back to ip scope if we have one.
		if c := ipToHostCIDR(ip); c != "" {
			return s.upsertTaskScope(TaskScope{TaskID: taskID, Kind: "ip", Net: c})
		}
	case "ip":
		if c := ipToHostCIDR(ip); c != "" {
			return s.upsertTaskScope(TaskScope{TaskID: taskID, Kind: "ip", Net: c})
		}
	}
	return nil
}

// AddAgentScope parses an agent-provided (kind,value) and records it (source='agent').
// company: value = company name or id (must already exist). root_domain/subdomain:
// value = a domain. ip/cidr: value = an IP or CIDR (bare IP → /32,/128).
func (s *AssetStore) AddAgentScope(taskID int64, kind, value, reason string) (TaskScope, error) {
	ts := TaskScope{TaskID: taskID, Kind: kind, Source: "agent", Reason: reason}
	if taskID <= 0 {
		return ts, fmt.Errorf("需要 task_id")
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return ts, fmt.Errorf("value 不能为空")
	}
	switch kind {
	case "company":
		if s.company == nil {
			return ts, fmt.Errorf("company store 未启用")
		}
		var comp *Company
		var err error
		if id, e := strconv.ParseInt(value, 10, 64); e == nil {
			comp, err = s.company.GetCompany(id)
		} else {
			comp, err = s.company.GetCompanyByName(value)
		}
		if err != nil {
			return ts, err
		}
		if comp == nil {
			return ts, fmt.Errorf("company 不存在: %s（先用 list_companies 确认，或建好企业）", value)
		}
		ts.CompanyID = &comp.ID
	case "root_domain":
		d := DomainKey(value)
		root, _ := RootDomain(d)
		if root == "" {
			root = d
		}
		if root == "" {
			return ts, fmt.Errorf("无效根域: %s", value)
		}
		ts.Domain = root
	case "subdomain":
		d := DomainKey(value)
		if d == "" {
			return ts, fmt.Errorf("无效子域: %s", value)
		}
		ts.Domain = d
	case "ip", "cidr":
		v := value
		if !strings.Contains(v, "/") {
			v = ipToHostCIDR(v)
			ts.Kind = "ip"
		} else {
			ts.Kind = "cidr"
		}
		if v == "" {
			return ts, fmt.Errorf("无效 ip/cidr: %s", value)
		}
		if _, _, err := net.ParseCIDR(v); err != nil {
			return ts, fmt.Errorf("无效 ip/cidr: %s", value)
		}
		ts.Net = v
	default:
		return ts, fmt.Errorf("不支持的 kind: %s（company/root_domain/subdomain/ip/cidr）", kind)
	}
	if err := s.upsertTaskScope(ts); err != nil {
		return ts, err
	}
	return ts, nil
}

// ListTaskScope returns all scope rows for a task.
func (s *AssetStore) ListTaskScope(taskID int64) ([]TaskScope, error) {
	rows, err := s.db.Query(`
SELECT id, kind, COALESCE(company_id,0), COALESCE(domain,''), COALESCE(net::text,''), source, COALESCE(reason,'')
FROM task_scope WHERE task_id=$1 ORDER BY id`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TaskScope{}
	for rows.Next() {
		var t TaskScope
		var cid int64
		if err := rows.Scan(&t.ID, &t.Kind, &cid, &t.Domain, &t.Net, &t.Source, &t.Reason); err != nil {
			return nil, err
		}
		t.TaskID = taskID
		if cid > 0 {
			t.CompanyID = &cid
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// CoverageAsset is one in-scope asset (used for the untested backlog sample).
type CoverageAsset struct {
	ID    int64  `json:"id"`
	Type  string `json:"type"`
	Label string `json:"label"`
}

// CoverageByType is per-asset-type coverage: total in scope vs tested.
type CoverageByType struct {
	Type   string `json:"type"`
	Total  int    `json:"total"`
	Tested int    `json:"tested"`
}

// Coverage is a task's rough asset test coverage — a reference figure for the agent,
// NOT a precise metric. Denominator = assets matching any active task_scope row;
// Tested = those anchored to at least one fact node in the exploration.
type Coverage struct {
	ScopeRows   int              `json:"scope_rows"`  // 0 → 范围未锚定
	Denominator int              `json:"denominator"` // 范围内资产数
	Tested      int              `json:"tested"`      // 已测(约)
	Pct         *float64         `json:"pct"`         // 覆盖度；分母 0 时 null
	ByType      []CoverageByType `json:"by_type"`     // 按资产类型的 总数/已测
}

// task_scope→assets match predicate, reused by the count / by-type / untested queries. $1=taskID.
const covTargetCTE = `
target AS (
  SELECT DISTINCT a.id, a.type,
         COALESCE(a.url, a.domain, a.ip, a.app_name, a.root_domain, '') AS label
  FROM assets a
  JOIN task_scope ts ON ts.task_id = $1 AND (
       (ts.kind='company'     AND a.company_id = ts.company_id)
    OR (ts.kind='root_domain' AND a.root_domain = ts.domain)
    OR (ts.kind='subdomain'   AND a.domain = ts.domain)
    OR (ts.kind IN ('ip','cidr') AND a.ip IS NOT NULL AND a.ip !~ '[^0-9a-fA-F:.]' AND ts.net >>= a.ip::inet)
  )
),
tested AS (
  SELECT DISTINCT ea.asset_id
  FROM exploration_anchors ea
  JOIN exploration_nodes en ON en.id = ea.node_id
  WHERE en.exploration_id = $2 AND en.kind = 'fact'
)`

// TaskCoverage computes rough per-type coverage for a task. taskID indexes
// task_scope + assets; expID indexes the fact anchors. Reference figure only.
func (s *AssetStore) TaskCoverage(taskID, expID int64) (*Coverage, error) {
	cov := &Coverage{ByType: []CoverageByType{}}
	_ = s.db.QueryRow(`SELECT count(*) FROM task_scope WHERE task_id=$1`, taskID).Scan(&cov.ScopeRows)
	rows, err := s.db.Query(`WITH `+covTargetCTE+`
SELECT t.type, count(*) AS total,
       count(*) FILTER (WHERE t.id IN (SELECT asset_id FROM tested)) AS tested
FROM target t GROUP BY t.type ORDER BY t.type`, taskID, expID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var bt CoverageByType
		if err := rows.Scan(&bt.Type, &bt.Total, &bt.Tested); err != nil {
			return nil, err
		}
		cov.ByType = append(cov.ByType, bt)
		cov.Denominator += bt.Total
		cov.Tested += bt.Tested
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if cov.Denominator > 0 {
		p := float64(cov.Tested) / float64(cov.Denominator)
		cov.Pct = &p
	}
	return cov, nil
}

// ListUntestedAssets returns a task's in-scope, not-yet-tested assets, optionally
// filtered by asset type, paginated. Returns the page + the total count. limit<=0 → 10.
func (s *AssetStore) ListUntestedAssets(taskID, expID int64, typ string, limit, offset int) ([]CoverageAsset, int, error) {
	if limit <= 0 {
		limit = 10
	}
	if offset < 0 {
		offset = 0
	}
	typeFilter := ""
	args := []any{taskID, expID}
	if typ != "" {
		typeFilter = " AND t.type = $3"
		args = append(args, typ)
	}
	var total int
	_ = s.db.QueryRow(`WITH `+covTargetCTE+`
SELECT count(*) FROM target t WHERE t.id NOT IN (SELECT asset_id FROM tested)`+typeFilter, args...).Scan(&total)
	pageArgs := append(append([]any{}, args...), limit, offset)
	limPos := strconv.Itoa(len(args) + 1)
	offPos := strconv.Itoa(len(args) + 2)
	rows, err := s.db.Query(`WITH `+covTargetCTE+`
SELECT t.id, t.type, t.label FROM target t
WHERE t.id NOT IN (SELECT asset_id FROM tested)`+typeFilter+`
ORDER BY t.id LIMIT $`+limPos+` OFFSET $`+offPos, pageArgs...)
	if err != nil {
		return nil, total, err
	}
	defer rows.Close()
	out := []CoverageAsset{}
	for rows.Next() {
		var a CoverageAsset
		if err := rows.Scan(&a.ID, &a.Type, &a.Label); err != nil {
			return nil, total, err
		}
		out = append(out, a)
	}
	return out, total, rows.Err()
}
