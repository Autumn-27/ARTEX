package db

import (
	"database/sql"
	"fmt"
	"net"
	"strconv"
	"strings"
)

// TaskScope is one row of a task's test scope — the coverage denominator and the
// per-task authorization edge. Rows come either from insertAssets (source='auto',
// conservative, one per explicitly-inserted asset) or from the add_task_scope tool
// (source='agent', for company / domain / network / ICP / keyword scope).
type TaskScope struct {
	ID        int64  `json:"id"`
	TaskID    int64  `json:"task_id"`
	Kind      string `json:"kind"` // company|root_domain|subdomain|ip|cidr|icp|keyword
	CompanyID *int64 `json:"company_id,omitempty"`
	// CompanyName is resolved for kind=company so callers can label a scope row
	// without a second lookup. Empty when the row is not a company reference.
	CompanyName string `json:"company_name,omitempty"`
	Domain      string `json:"domain,omitempty"`
	Net         string `json:"net,omitempty"`
	Value       string `json:"value,omitempty"`
	Source      string `json:"source"`
	Reason      string `json:"reason,omitempty"`
}

// stripHostPort drops a trailing :port from a host:port / ip:port / [ipv6]:port
// value, returning the bare host. Bare hosts, bare IPs (v4 or v6, whose colons make
// them ambiguous), and anything not in host:port form are returned unchanged. Scope
// is host/net based, so the port from a target like "10.0.188.136:3000" or
// "api.example.com:8080" is simply discarded rather than baked into the key.
func stripHostPort(v string) string {
	v = strings.TrimSpace(v)
	if host, _, err := net.SplitHostPort(v); err == nil {
		return host
	}
	return v
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
func (s *AssetStore) upsertTaskScopeResult(ts TaskScope) (bool, error) {
	if ts.TaskID <= 0 || ts.Kind == "" {
		return false, nil
	}
	var domainVal, netVal, companyVal, valueVal any
	if ts.Domain != "" {
		domainVal = ts.Domain
	}
	if ts.Net != "" {
		netVal = ts.Net
	}
	if ts.CompanyID != nil && *ts.CompanyID > 0 {
		companyVal = *ts.CompanyID
	}
	if ts.Value != "" {
		valueVal = ts.Value
	}
	src := ts.Source
	if src == "" {
		src = "auto"
	}
	query := `
INSERT INTO task_scope(task_id, kind, company_id, domain, net, value, source, reason)
VALUES ($1,$2,$3,$4,$5::cidr,$6,$7,NULLIF($8,''))
ON CONFLICT DO NOTHING`
	var (
		result sql.Result
		err    error
	)
	if s.tx != nil {
		result, err = s.tx.Exec(query, ts.TaskID, ts.Kind, companyVal, domainVal, netVal, valueVal, src, ts.Reason)
	} else {
		result, err = s.db.Exec(query, ts.TaskID, ts.Kind, companyVal, domainVal, netVal, valueVal, src, ts.Reason)
	}
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	return rows > 0, err
}

func (s *AssetStore) upsertTaskScope(ts TaskScope) error {
	_, err := s.upsertTaskScopeResult(ts)
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

// AddAgentScope parses a (kind,value) pair and records it as task scope.
// source is "agent" when called from an LLM tool, "manual" from the UI.
// company: value = company name or id (must already exist). root_domain/subdomain:
// value = a domain. ip/cidr: value = an IP or CIDR (bare IP → /32,/128).
func (s *AssetStore) AddAgentScope(taskID int64, kind, value, reason, source string) (TaskScope, error) {
	if source == "" {
		source = "agent"
	}
	ts := TaskScope{TaskID: taskID, Kind: kind, Source: source, Reason: reason}
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
		d := DomainKey(stripHostPort(value))
		root, _ := RootDomain(d)
		if root == "" {
			root = d
		}
		if root == "" {
			return ts, fmt.Errorf("无效根域: %s", value)
		}
		ts.Domain = root
	case "subdomain":
		d := DomainKey(stripHostPort(value))
		if d == "" {
			return ts, fmt.Errorf("无效子域: %s", value)
		}
		ts.Domain = d
	case "ip", "cidr":
		v := value
		if !strings.Contains(v, "/") {
			v = ipToHostCIDR(stripHostPort(v))
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
	case "icp", "keyword":
		parsed, err := ParseScopeInput(ScopeInput{Kind: kind, Value: value})
		if err != nil {
			return ts, err
		}
		ts.Value = parsed.Value
	default:
		return ts, fmt.Errorf("不支持的 kind: %s（company/root_domain/subdomain/ip/cidr/icp/keyword）", kind)
	}
	if err := s.upsertTaskScope(ts); err != nil {
		return ts, err
	}
	return ts, nil
}

// DeleteTaskScope removes a single scope row by id, scoped to the given task.
// Returns whether a row was actually deleted.
func (s *AssetStore) DeleteTaskScope(taskID, scopeID int64) (bool, error) {
	res, err := s.db.Exec(`DELETE FROM task_scope WHERE id=$1 AND task_id=$2`, scopeID, taskID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ListTaskScope returns all scope rows for a task.
func (s *AssetStore) ListTaskScope(taskID int64) ([]TaskScope, error) {
	rows, err := s.db.Query(`
SELECT ts.id, ts.kind, COALESCE(ts.company_id,0), COALESCE(c.name,''), COALESCE(ts.domain,''),
       COALESCE(ts.net::text,''), COALESCE(ts.value,''), ts.source, COALESCE(ts.reason,'')
FROM task_scope ts
LEFT JOIN companies c ON c.id=ts.company_id
WHERE ts.task_id=$1 ORDER BY ts.id`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TaskScope{}
	for rows.Next() {
		var t TaskScope
		var cid int64
		if err := rows.Scan(&t.ID, &t.Kind, &cid, &t.CompanyName, &t.Domain, &t.Net, &t.Value, &t.Source, &t.Reason); err != nil {
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
	Enabled     bool             `json:"enabled"`     // 资产覆盖度功能是否开启；false 时其余字段为零值
	ScopeRows   int              `json:"scope_rows"`  // 0 → 范围未锚定
	Denominator int              `json:"denominator"` // 范围内资产数
	Tested      int              `json:"tested"`      // 已测(约)
	Pct         *float64         `json:"pct"`         // 覆盖度；分母 0 时 null
	ByType      []CoverageByType `json:"by_type"`     // 按资产类型的 总数/已测
}

// CoverageEnabled reports whether a task has the asset-coverage feature turned on
// (tasks.coverage_enabled). Missing row / error → true (fail open to the default),
// so unknown/legacy tasks keep the historical behavior. taskID<=0 → true.
func (s *AssetStore) CoverageEnabled(taskID int64) bool {
	if taskID <= 0 {
		return true
	}
	var enabled bool
	if err := s.db.QueryRow(`SELECT COALESCE(coverage_enabled,true) FROM tasks WHERE id=$1`, taskID).Scan(&enabled); err != nil {
		return true
	}
	return enabled
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
    OR (ts.kind IN ('ip','cidr') AND ts.net >>= try_inet(a.ip))
    OR (ts.kind='icp' AND (
         lower(regexp_replace(COALESCE(a.icp,''), '[[:space:]]+', '', 'g')) = ts.value
         OR lower(regexp_replace(COALESCE(a.app_icp,''), '[[:space:]]+', '', 'g')) = ts.value
       ))
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

// ---------------------------------------------------------------------------
// Coverage graph — a force-directed view of a task's in-scope assets.
// ---------------------------------------------------------------------------

// CoverageGraphNode is one node of the asset coverage graph. Node identity is a
// string key: an asset row is "a:<id>", a company is "c:<id>", and a root domain
// with no asset row of its own is the synthetic "r:<domain>". Out-of-scope
// connector nodes (roots pulled in only to link subdomains, and the companies
// above them) carry InScope=false and are rendered gray.
type CoverageGraphNode struct {
	Key         string `json:"key"`
	Kind        string `json:"kind"` // company|root_domain|subdomain|ip|service|app|endpoint
	Label       string `json:"label"`
	Tested      bool   `json:"tested"`
	InScope     bool   `json:"in_scope"`
	AssetID     int64  `json:"asset_id,omitempty"` // 0 for company / synthetic root
	CompanyID   int64  `json:"company_id,omitempty"`
	Domain      string `json:"domain,omitempty"`
	RootDomain  string `json:"root_domain,omitempty"`
	IP          string `json:"ip,omitempty"`
	URL         string `json:"url,omitempty"`
	Port        int    `json:"port,omitempty"`
	ServiceType string `json:"service_type,omitempty"`
	AppName     string `json:"app_name,omitempty"`
	PageTitle   string `json:"page_title,omitempty"`
	StatusCode  int    `json:"status_code,omitempty"`
}

// CoverageGraphEdge is a child→parent containment link (endpoint→service→
// subdomain/ip→root_domain→company, app→company).
type CoverageGraphEdge struct {
	Src string `json:"src"`
	Dst string `json:"dst"`
}

// CoverageGraphData is the whole graph for one task.
type CoverageGraphData struct {
	Nodes []CoverageGraphNode `json:"nodes"`
	Edges []CoverageGraphEdge `json:"edges"`
}

func assetKey(id int64) string   { return "a:" + strconv.FormatInt(id, 10) }
func companyKey(id int64) string { return "c:" + strconv.FormatInt(id, 10) }

// hostPortOf returns the (host, port) a service / endpoint node hangs off — the
// domain if set, otherwise the URL host, otherwise the IP.
func hostPortOf(n *CoverageGraphNode) (string, int) {
	host, port := n.Domain, n.Port
	if host == "" && n.URL != "" {
		h, p, _ := parseURL(normalizeURL(n.URL))
		host = h
		if port == 0 {
			port = p
		}
	}
	if host == "" {
		host = n.IP
	}
	return host, port
}

// BuildCoverageGraph assembles the full coverage graph for a task and its direct
// read-only sources: every in-scope asset plus connector root domains/companies,
// with current-or-source fact anchors reflected in Tested. The legacy expID
// argument is retained for API compatibility; the task registry is authoritative.
func (s *AssetStore) BuildCoverageGraph(taskID, _ int64) (*CoverageGraphData, error) {
	g := &CoverageGraphData{Nodes: []CoverageGraphNode{}, Edges: []CoverageGraphEdge{}}
	if taskID <= 0 {
		return g, nil
	}
	rows, err := s.db.Query(`WITH `+contextCoverageCTE+`
SELECT a.id, a.type, COALESCE(a.company_id,0),
       COALESCE(a.domain,''), COALESCE(a.root_domain,''), COALESCE(a.ip,''),
       COALESCE(a.url,''), COALESCE(a.port,0), COALESCE(a.service_type,''),
       COALESCE(a.app_name,''), COALESCE(a.page_title,''), COALESCE(a.status_code,0),
       (a.id IN (SELECT asset_id FROM tested)) AS tested
FROM assets a JOIN target t ON t.id = a.id
ORDER BY a.id`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	byKey := map[string]*CoverageGraphNode{}
	rootByDomain := map[string]string{} // root domain → node key
	subByDomain := map[string]string{}  // subdomain    → node key
	ipByAddr := map[string]string{}     // ip literal   → node key
	svcByHostPort := map[string]string{}
	svcByHost := map[string]string{}
	companyIDs := map[int64]bool{} // referenced company ids (need a node)

	add := func(n CoverageGraphNode) *CoverageGraphNode {
		if _, ok := byKey[n.Key]; ok {
			return byKey[n.Key]
		}
		g.Nodes = append(g.Nodes, n)
		p := &g.Nodes[len(g.Nodes)-1]
		byKey[n.Key] = p
		return p
	}

	for rows.Next() {
		var n CoverageGraphNode
		var companyID int64
		if err := rows.Scan(&n.AssetID, &n.Kind, &companyID,
			&n.Domain, &n.RootDomain, &n.IP, &n.URL, &n.Port, &n.ServiceType,
			&n.AppName, &n.PageTitle, &n.StatusCode, &n.Tested); err != nil {
			return nil, err
		}
		n.Key = assetKey(n.AssetID)
		n.CompanyID = companyID
		n.InScope = true
		n.Label = coverageNodeLabel(&n)
		p := add(n)
		switch n.Kind {
		case "root_domain":
			if n.Domain != "" {
				rootByDomain[n.Domain] = p.Key
			}
		case "subdomain":
			if n.Domain != "" {
				subByDomain[n.Domain] = p.Key
			}
		case "ip":
			if n.IP != "" {
				ipByAddr[n.IP] = p.Key
			}
		case "service":
			host, port := hostPortOf(p)
			if host != "" {
				svcByHost[host] = p.Key
				svcByHostPort[host+"|"+strconv.Itoa(port)] = p.Key
			}
		}
		if companyID > 0 {
			companyIDs[companyID] = true
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Connector root domains: any subdomain whose root domain is not itself an
	// in-scope node. Pull the real asset row if one exists (so the drawer shows
	// real detail), else synthesize a bare "r:<domain>" placeholder. Both gray.
	missingRoots := map[string]bool{}
	for _, n := range g.Nodes {
		if n.Kind == "subdomain" && n.RootDomain != "" {
			if _, ok := rootByDomain[n.RootDomain]; !ok {
				missingRoots[n.RootDomain] = true
			}
		}
	}
	for root := range missingRoots {
		var id, companyID int64
		err := s.db.QueryRow(`SELECT id, COALESCE(company_id,0) FROM assets
WHERE type='root_domain' AND domain=$1 LIMIT 1`, root).Scan(&id, &companyID)
		var node CoverageGraphNode
		if err == nil && id > 0 {
			node = CoverageGraphNode{Key: assetKey(id), Kind: "root_domain", AssetID: id,
				CompanyID: companyID, Domain: root, Label: root}
			if companyID > 0 {
				companyIDs[companyID] = true
			}
		} else {
			node = CoverageGraphNode{Key: "r:" + root, Kind: "root_domain", Domain: root, Label: root}
		}
		add(node)
		rootByDomain[root] = node.Key
	}

	// Company nodes for every referenced company id — always gray context.
	for id := range companyIDs {
		key := companyKey(id)
		if _, ok := byKey[key]; ok {
			continue
		}
		var name string
		if err := s.db.QueryRow(`SELECT name FROM companies WHERE id=$1`, id).Scan(&name); err != nil {
			continue
		}
		add(CoverageGraphNode{Key: key, Kind: "company", CompanyID: id,
			Label: name, AssetID: 0})
	}

	// Derived containment edges (only when the parent node exists).
	link := func(childKey, parentKey string) {
		if parentKey == "" || parentKey == childKey {
			return
		}
		if _, ok := byKey[parentKey]; !ok {
			return
		}
		g.Edges = append(g.Edges, CoverageGraphEdge{Src: childKey, Dst: parentKey})
	}
	firstOf := func(keys ...string) string {
		for _, k := range keys {
			if k != "" {
				if _, ok := byKey[k]; ok {
					return k
				}
			}
		}
		return ""
	}
	for i := range g.Nodes {
		n := &g.Nodes[i]
		switch n.Kind {
		case "subdomain":
			link(n.Key, rootByDomain[n.RootDomain])
		case "root_domain", "app", "ip":
			if n.CompanyID > 0 {
				link(n.Key, companyKey(n.CompanyID))
			}
		case "service":
			link(n.Key, firstOf(subByDomain[n.Domain], ipByAddr[n.IP], rootByDomain[n.RootDomain]))
		case "endpoint":
			host, port := hostPortOf(n)
			parent := firstOf(
				svcByHostPort[host+"|"+strconv.Itoa(port)], svcByHost[host],
				subByDomain[host], subByDomain[n.Domain], ipByAddr[host], ipByAddr[n.IP],
				rootByDomain[n.RootDomain])
			link(n.Key, parent)
		}
	}
	return g, nil
}

// coverageNodeLabel picks the human label for a coverage-graph node.
func coverageNodeLabel(n *CoverageGraphNode) string {
	switch n.Kind {
	case "endpoint", "service":
		if n.URL != "" {
			return n.URL
		}
	case "app":
		if n.AppName != "" {
			return n.AppName
		}
	}
	for _, v := range []string{n.URL, n.Domain, n.IP, n.AppName, n.RootDomain} {
		if v != "" {
			return v
		}
	}
	return n.Key
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
