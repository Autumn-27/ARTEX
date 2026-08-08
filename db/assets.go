package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"strings"
)

// =====================================================================
// 统一资产表
// =====================================================================

// Asset is a row in the assets table.
type Asset struct {
	ID          int64   `json:"id"`
	Type        string  `json:"type"`
	CompanyID   *int64  `json:"company_id,omitempty"`
	TaskIDs     []int64 `json:"task_ids"`
	Domain      string  `json:"domain,omitempty"`
	RootDomain  string  `json:"root_domain,omitempty"`
	IP          string  `json:"ip,omitempty"`
	CSegment    string  `json:"c_segment,omitempty"`
	Port        *int    `json:"port,omitempty"`
	ICP         string  `json:"icp,omitempty"`
	// ip fields
	BoundDomains []string         `json:"bound_domains,omitempty"`
	OpenPorts    []map[string]any `json:"open_ports,omitempty"`
	// subdomain fields
	RecordType  string   `json:"record_type,omitempty"`
	RecordValue []string `json:"record_value,omitempty"`
	// app fields
	BundleID       string `json:"bundle_id,omitempty"`
	AppName        string `json:"app_name,omitempty"`
	Category       string `json:"category,omitempty"`
	AppDescription string `json:"app_description,omitempty"`
	AppICP         string `json:"app_icp,omitempty"`
	// service fields
	URL           string           `json:"url,omitempty"`
	ServiceType   string           `json:"service_type,omitempty"`
	ServiceName   string           `json:"service_name,omitempty"`
	FaviconMMH3   string           `json:"favicon_mmh3,omitempty"`
	StatusCode    *int             `json:"status_code,omitempty"`
	ContentLength *int64           `json:"content_length,omitempty"`
	PageTitle     string           `json:"page_title,omitempty"`
	Technologies  []string         `json:"technologies,omitempty"`
	Auth          []map[string]any `json:"auth,omitempty"`
	// endpoint fields
	Method string           `json:"method,omitempty"`
	Params []map[string]any `json:"params,omitempty"`
	// meta
	Extra    map[string]any `json:"extra,omitempty"`
	LastSeen string         `json:"last_seen"`
}

// AuthItem is one entry in the auth array.
type AuthItem struct {
	Type        string `json:"type,omitempty"`
	Username    string `json:"username,omitempty"`
	Password    string `json:"password,omitempty"`
	Token       string `json:"token,omitempty"`
	Description string `json:"description,omitempty"`
}

// ParamItem is one entry in the params array.
type ParamItem struct {
	Location string `json:"location"`
	Name     string `json:"name"`
	Value    string `json:"value,omitempty"`
	Type     string `json:"type,omitempty"`
}

// PortService is one entry in open_ports: {"port":22,"service":"ssh"}.
type PortService struct {
	Port    int    `json:"port"`
	Service string `json:"service,omitempty"`
}

// AssetStore operates on the assets table.
type AssetStore struct {
	db      *DB
	company *CompanyStore
}

// Assets returns the asset store.
func (d *DB) Assets() *AssetStore {
	return &AssetStore{db: d, company: d.Companies()}
}

// Companies returns the company store associated with this asset store.
func (s *AssetStore) Companies() *CompanyStore { return s.company }

// =====================================================================
// Helpers
// =====================================================================

// calcCSegment computes the /24 (IPv4) or /48 (IPv6) network for an IP string.
func calcCSegment(ipStr string) string {
	if ipStr == "" {
		return ""
	}
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return ""
	}
	if ip.To4() != nil {
		// IPv4 /24
		parts := strings.Split(ipStr, ".")
		if len(parts) == 4 {
			return parts[0] + "." + parts[1] + "." + parts[2] + ".0/24"
		}
		return ""
	}
	// IPv6 /48
	_, ipnet, err := net.ParseCIDR(ipStr + "/48")
	if err != nil {
		return ""
	}
	return ipnet.String()
}

// normalizeURL lowercases scheme and host, strips trailing slash from bare roots.
func normalizeURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	s := u.String()
	if strings.HasSuffix(s, "/") && u.Path == "/" && u.RawQuery == "" {
		s = strings.TrimSuffix(s, "/")
	}
	return s
}

// parseURL extracts domain, port, service_name from a URL.
func parseURL(raw string) (domain string, port int, serviceName string) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", 0, ""
	}
	domain = strings.ToLower(u.Hostname())
	port = defaultPort(strings.ToLower(u.Scheme), u.Port())
	switch strings.ToLower(u.Scheme) {
	case "https":
		serviceName = "HTTPS"
	case "http":
		serviceName = "HTTP"
	default:
		serviceName = strings.ToUpper(u.Scheme)
	}
	return
}

func marshalJSONBArray(items []map[string]any) (string, error) {
	if len(items) == 0 {
		return "{}", nil
	}
	parts := make([]string, len(items))
	for i, m := range items {
		b, err := json.Marshal(m)
		if err != nil {
			return "", err
		}
		// PostgreSQL array literal for jsonb[]: each element must be
		// double-quoted with internal backslashes and double-quotes escaped.
		s := strings.ReplaceAll(string(b), `\`, `\\`)
		s = strings.ReplaceAll(s, `"`, `\"`)
		parts[i] = `"` + s + `"`
	}
	return "{" + strings.Join(parts, ",") + "}", nil
}

func marshalStringArray(items []string) string {
	if len(items) == 0 {
		return "{}"
	}
	escaped := make([]string, len(items))
	for i, s := range items {
		escaped[i] = `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
	}
	return "{" + strings.Join(escaped, ",") + "}"
}

func marshalPortServices(ps []PortService) (string, error) {
	items := make([]map[string]any, len(ps))
	for i, p := range ps {
		items[i] = map[string]any{"port": p.Port, "service": p.Service}
	}
	return marshalJSONBArray(items)
}

// nullableInt64 returns sql.NullInt64.
func nullableInt(v int) interface{} {
	if v == 0 {
		return nil
	}
	return v
}

// =====================================================================
// UpsertRootDomain
// =====================================================================

// UpsertRootDomainReq is the input for UpsertRootDomain.
type UpsertRootDomainReq struct {
	Domain string
	ICP    string
	TaskID int64
}

// UpsertRootDomain idempotently inserts or merges a root domain asset.
func (s *AssetStore) UpsertRootDomain(req UpsertRootDomainReq) (int64, error) {
	domain := DomainKey(req.Domain)
	if domain == "" {
		return 0, fmt.Errorf("domain is required")
	}
	companyID, err := s.company.ResolveCompany(domain, "")
	if err != nil {
		return 0, err
	}

	var taskIDs string
	if req.TaskID > 0 {
		taskIDs = fmt.Sprintf("{%d}", req.TaskID)
	} else {
		taskIDs = "{}"
	}

	var icpVal any
	if req.ICP != "" {
		icpVal = req.ICP
	}

	var id int64
	err = s.db.QueryRow(`
INSERT INTO assets(type, domain, root_domain, icp, company_id, task_ids)
VALUES ('root_domain', $1, $1, $2, $3, $4::bigint[])
ON CONFLICT (domain) WHERE type = 'root_domain' DO UPDATE SET
    icp        = COALESCE(EXCLUDED.icp, assets.icp),
    company_id = COALESCE(EXCLUDED.company_id, assets.company_id),
    task_ids   = (SELECT ARRAY(SELECT DISTINCT unnest(assets.task_ids || EXCLUDED.task_ids))),
    extra      = assets.extra || EXCLUDED.extra,
    last_seen  = now()
RETURNING id`, domain, icpVal, companyID, taskIDs).Scan(&id)
	return id, err
}

// =====================================================================
// UpsertIP
// =====================================================================

// UpsertIPReq is the input for UpsertIP.
type UpsertIPReq struct {
	IP           string
	BoundDomains []string
	OpenPorts    []PortService
	TaskID       int64
}

// UpsertIP idempotently inserts or merges an IP asset.
func (s *AssetStore) UpsertIP(req UpsertIPReq) (int64, error) {
	if req.IP == "" {
		return 0, fmt.Errorf("ip is required")
	}
	cseg := calcCSegment(req.IP)
	companyID, err := s.company.ResolveCompany("", req.IP)
	if err != nil {
		return 0, err
	}

	var taskIDs string
	if req.TaskID > 0 {
		taskIDs = fmt.Sprintf("{%d}", req.TaskID)
	} else {
		taskIDs = "{}"
	}

	boundDomains := marshalStringArray(req.BoundDomains)
	openPortsJSON, err := marshalPortServices(req.OpenPorts)
	if err != nil {
		return 0, err
	}

	var csegVal any
	if cseg != "" {
		csegVal = cseg
	}

	var id int64
	err = s.db.QueryRow(`
INSERT INTO assets(type, ip, c_segment, bound_domains, open_ports, company_id, task_ids)
VALUES ('ip', $1, $2::cidr, $3::text[], $4::jsonb[], $5, $6::bigint[])
ON CONFLICT (ip) WHERE type = 'ip' DO UPDATE SET
    bound_domains = (SELECT ARRAY(SELECT DISTINCT unnest(assets.bound_domains || EXCLUDED.bound_domains))),
    open_ports    = (
        SELECT ARRAY(
            SELECT DISTINCT ON ((elem->>'port')::int) elem
            FROM unnest(assets.open_ports || EXCLUDED.open_ports) AS elem
            ORDER BY (elem->>'port')::int,
                     CASE WHEN elem->>'service' IS NOT NULL AND elem->>'service' <> '' THEN 0 ELSE 1 END
        )
    ),
    c_segment  = COALESCE(assets.c_segment, EXCLUDED.c_segment),
    company_id = COALESCE(EXCLUDED.company_id, assets.company_id),
    task_ids   = (SELECT ARRAY(SELECT DISTINCT unnest(assets.task_ids || EXCLUDED.task_ids))),
    last_seen  = now()
RETURNING id`, req.IP, csegVal, boundDomains, openPortsJSON, companyID, taskIDs).Scan(&id)
	return id, err
}

// AppendIPPort appends a {port, service} entry to an existing IP asset's open_ports.
func (s *AssetStore) AppendIPPort(ipStr string, port int, serviceName string) error {
	if ipStr == "" || port == 0 {
		return nil
	}
	entry, _ := json.Marshal(map[string]any{"port": port, "service": serviceName})
	_, err := s.db.Exec(`
UPDATE assets SET
    open_ports = (
        SELECT ARRAY(
            SELECT DISTINCT ON ((elem->>'port')::int) elem
            FROM unnest(open_ports || ARRAY[$1::jsonb]) AS elem
            ORDER BY (elem->>'port')::int,
                     CASE WHEN elem->>'service' IS NOT NULL AND elem->>'service' <> '' THEN 0 ELSE 1 END
        )
    ),
    last_seen = now()
WHERE type = 'ip' AND ip = $2`, string(entry), ipStr)
	return err
}

// AppendIPBoundDomain appends a domain to an existing IP asset's bound_domains.
func (s *AssetStore) AppendIPBoundDomain(ipStr, domain string) error {
	if ipStr == "" || domain == "" {
		return nil
	}
	_, err := s.db.Exec(`
UPDATE assets SET
    bound_domains = (SELECT ARRAY(SELECT DISTINCT unnest(bound_domains || ARRAY[$1::text]))),
    last_seen = now()
WHERE type = 'ip' AND ip = $2`, domain, ipStr)
	return err
}

// =====================================================================
// UpsertSubdomain
// =====================================================================

// UpsertSubdomainReq is the input for UpsertSubdomain.
type UpsertSubdomainReq struct {
	Domain      string
	RecordType  string
	RecordValue []string
	ICP         string
	TaskID      int64
}

// UpsertSubdomain idempotently inserts or merges a subdomain asset and triggers
// side effects: root_domain upsert + IP bound_domains update.
func (s *AssetStore) UpsertSubdomain(req UpsertSubdomainReq) (id int64, err error) {
	domain := DomainKey(req.Domain)
	if domain == "" {
		return 0, fmt.Errorf("domain is required")
	}

	rootDomain, _ := RootDomain(domain)
	if rootDomain == "" {
		rootDomain = domain
	}

	// resolve company by root domain
	companyID, err := s.company.ResolveCompany(rootDomain, "")
	if err != nil {
		return 0, err
	}

	// side effect 1: ensure root domain exists
	_, _ = s.UpsertRootDomain(UpsertRootDomainReq{
		Domain: rootDomain,
		TaskID: req.TaskID,
	})

	// side effect 2: if A/AAAA record, upsert each IP + bind domain.
	// RecordValue is []string; each element may itself be comma-separated (legacy).
	var ipStr string // first valid IP, used for the subdomain row itself
	if req.RecordType == "A" || req.RecordType == "AAAA" {
		for _, rv := range req.RecordValue {
			for _, part := range strings.Split(rv, ",") {
				candidate := strings.TrimSpace(part)
				if candidate == "" || net.ParseIP(candidate) == nil {
					continue
				}
				if ipStr == "" {
					ipStr = candidate
				}
				_, _ = s.UpsertIP(UpsertIPReq{
					IP:           candidate,
					BoundDomains: []string{domain},
					TaskID:       req.TaskID,
				})
			}
		}
	}

	cseg := calcCSegment(ipStr)
	if companyID == nil && ipStr != "" {
		companyID, _ = s.company.ResolveCompany("", ipStr)
	}

	var taskIDs string
	if req.TaskID > 0 {
		taskIDs = fmt.Sprintf("{%d}", req.TaskID)
	} else {
		taskIDs = "{}"
	}

	var icpVal, csegVal, ipVal any
	if req.ICP != "" {
		icpVal = req.ICP
	}
	if cseg != "" {
		csegVal = cseg
	}
	if ipStr != "" {
		ipVal = ipStr
	}
	recordType := req.RecordType
	recordValueArr := marshalStringArray(req.RecordValue)

	err = s.db.QueryRow(`
INSERT INTO assets(type, domain, root_domain, record_type, record_value, ip, c_segment, icp, company_id, task_ids)
VALUES ('subdomain', $1, $2, $3, $4::text[], $5, $6::cidr, $7, $8, $9::bigint[])
ON CONFLICT (domain, COALESCE(record_type,'')) WHERE type = 'subdomain' DO UPDATE SET
    ip           = COALESCE(EXCLUDED.ip, assets.ip),
    c_segment    = COALESCE(EXCLUDED.c_segment, assets.c_segment),
    icp          = COALESCE(EXCLUDED.icp, assets.icp),
    company_id   = COALESCE(EXCLUDED.company_id, assets.company_id),
    task_ids     = (SELECT ARRAY(SELECT DISTINCT unnest(assets.task_ids || EXCLUDED.task_ids))),
    record_value = (SELECT ARRAY(SELECT DISTINCT unnest(assets.record_value || EXCLUDED.record_value))),
    extra        = assets.extra || EXCLUDED.extra,
    last_seen    = now()
RETURNING id`, domain, rootDomain, recordType, recordValueArr, ipVal, csegVal, icpVal, companyID, taskIDs).Scan(&id)
	return id, err
}

// =====================================================================
// UpsertApp
// =====================================================================

// UpsertAppReq is the input for UpsertApp.
type UpsertAppReq struct {
	Name        string
	BundleID    string
	Category    string
	Description string
	ICP         string
	CompanyID   *int64 // explicit override; nil = no auto attribution for apps
	TaskID      int64
}

// UpsertApp idempotently inserts or merges an app asset.
func (s *AssetStore) UpsertApp(req UpsertAppReq) (int64, error) {
	if req.Name == "" {
		return 0, fmt.Errorf("app name is required")
	}

	var taskIDs string
	if req.TaskID > 0 {
		taskIDs = fmt.Sprintf("{%d}", req.TaskID)
	} else {
		taskIDs = "{}"
	}

	var bundleVal, catVal, descVal, icpVal, companyIDVal any
	if req.BundleID != "" {
		bundleVal = req.BundleID
	}
	if req.Category != "" {
		catVal = req.Category
	}
	if req.Description != "" {
		descVal = req.Description
	}
	if req.ICP != "" {
		icpVal = req.ICP
	}
	if req.CompanyID != nil {
		companyIDVal = *req.CompanyID
	}

	var id int64
	var err error

	if req.BundleID != "" {
		err = s.db.QueryRow(`
INSERT INTO assets(type, bundle_id, app_name, category, app_description, app_icp, company_id, task_ids)
VALUES ('app', $1, $2, $3, $4, $5, $6, $7::bigint[])
ON CONFLICT (bundle_id) WHERE type = 'app' AND bundle_id IS NOT NULL DO UPDATE SET
    app_name        = COALESCE(EXCLUDED.app_name, assets.app_name),
    category        = COALESCE(EXCLUDED.category, assets.category),
    app_description = COALESCE(EXCLUDED.app_description, assets.app_description),
    app_icp         = COALESCE(EXCLUDED.app_icp, assets.app_icp),
    company_id      = COALESCE(EXCLUDED.company_id, assets.company_id),
    task_ids        = (SELECT ARRAY(SELECT DISTINCT unnest(assets.task_ids || EXCLUDED.task_ids))),
    last_seen       = now()
RETURNING id`, bundleVal, req.Name, catVal, descVal, icpVal, companyIDVal, taskIDs).Scan(&id)
	} else {
		err = s.db.QueryRow(`
INSERT INTO assets(type, bundle_id, app_name, category, app_description, app_icp, company_id, task_ids)
VALUES ('app', NULL, $1, $2, $3, $4, $5, $6::bigint[])
ON CONFLICT (app_name) WHERE type = 'app' AND bundle_id IS NULL DO UPDATE SET
    category        = COALESCE(EXCLUDED.category, assets.category),
    app_description = COALESCE(EXCLUDED.app_description, assets.app_description),
    app_icp         = COALESCE(EXCLUDED.app_icp, assets.app_icp),
    company_id      = COALESCE(EXCLUDED.company_id, assets.company_id),
    task_ids        = (SELECT ARRAY(SELECT DISTINCT unnest(assets.task_ids || EXCLUDED.task_ids))),
    last_seen       = now()
RETURNING id`, req.Name, catVal, descVal, icpVal, companyIDVal, taskIDs).Scan(&id)
	}
	return id, err
}

// =====================================================================
// UpsertHTTPService
// =====================================================================

// UpsertHTTPServiceReq is the input for UpsertHTTPService.
type UpsertHTTPServiceReq struct {
	URL           string
	Technologies  []string
	StatusCode    *int
	ContentLength *int64
	PageTitle     string
	FaviconMMH3   string
	Auth          []map[string]any
	IP            string // optional, from async DNS
	TaskID        int64
}

// UpsertHTTPService inserts or merges an HTTP service asset. Domain, port,
// service_name, and root_domain are auto-extracted from URL.
func (s *AssetStore) UpsertHTTPService(req UpsertHTTPServiceReq) (int64, error) {
	if req.URL == "" {
		return 0, fmt.Errorf("url is required")
	}
	normURL := normalizeURL(req.URL)
	domain, port, serviceName := parseURL(normURL)
	rootDomain, _ := RootDomain(domain)
	if rootDomain == "" {
		rootDomain = domain
	}

	cseg := calcCSegment(req.IP)
	companyID, err := s.company.ResolveCompany(rootDomain, req.IP)
	if err != nil {
		return 0, err
	}

	var taskIDs string
	if req.TaskID > 0 {
		taskIDs = fmt.Sprintf("{%d}", req.TaskID)
	} else {
		taskIDs = "{}"
	}

	techsArr := marshalStringArray(req.Technologies)
	authJSON, err := marshalJSONBArray(req.Auth)
	if err != nil {
		return 0, err
	}

	var domainVal, ipVal, csegVal, titleVal, faviconVal any
	if domain != "" {
		domainVal = domain
	}
	if req.IP != "" {
		ipVal = req.IP
	}
	if cseg != "" {
		csegVal = cseg
	}
	if req.PageTitle != "" {
		titleVal = req.PageTitle
	}
	if req.FaviconMMH3 != "" {
		faviconVal = req.FaviconMMH3
	}

	var id int64
	err = s.db.QueryRow(`
INSERT INTO assets(
    type, url, service_type, service_name, domain, ip, port, root_domain,
    c_segment, favicon_mmh3, technologies, status_code, content_length, page_title,
    auth, company_id, task_ids
)
VALUES (
    'service', $1, 'http', $2, $3, $4, $5, $6,
    $7::cidr, $8, $9::text[], $10, $11, $12,
    $13::jsonb[], $14, $15::bigint[]
)
ON CONFLICT (url) WHERE type = 'service' AND service_type = 'http' DO UPDATE SET
    status_code    = COALESCE(EXCLUDED.status_code,    assets.status_code),
    content_length = COALESCE(EXCLUDED.content_length, assets.content_length),
    page_title     = COALESCE(EXCLUDED.page_title,     assets.page_title),
    favicon_mmh3   = COALESCE(EXCLUDED.favicon_mmh3,   assets.favicon_mmh3),
    ip             = COALESCE(EXCLUDED.ip,             assets.ip),
    c_segment      = COALESCE(EXCLUDED.c_segment,      assets.c_segment),
    technologies   = (SELECT ARRAY(SELECT DISTINCT unnest(assets.technologies || EXCLUDED.technologies))),
    auth           = (
        SELECT ARRAY(
            SELECT DISTINCT ON (elem::text) elem
            FROM unnest(assets.auth || EXCLUDED.auth) AS elem
        )
    ),
    task_ids       = (SELECT ARRAY(SELECT DISTINCT unnest(assets.task_ids || EXCLUDED.task_ids))),
    company_id     = COALESCE(EXCLUDED.company_id, assets.company_id),
    last_seen      = now()
RETURNING id`,
		normURL, serviceName, domainVal, ipVal, nullableInt(port), rootDomain,
		csegVal, faviconVal, techsArr, req.StatusCode, req.ContentLength, titleVal,
		authJSON, companyID, taskIDs,
	).Scan(&id)
	if err != nil {
		return 0, err
	}

	// side effects: register root_domain + subdomain as their own assets too
	s.linkHostAssets(domain, rootDomain, req.TaskID)
	if req.IP != "" {
		var boundDomains []string
		if domain != "" {
			boundDomains = []string{domain}
		}
		var openPorts []PortService
		if port > 0 {
			openPorts = []PortService{{Port: port, Service: serviceName}}
		}
		_, _ = s.UpsertIP(UpsertIPReq{
			IP:           req.IP,
			BoundDomains: boundDomains,
			OpenPorts:    openPorts,
			TaskID:       req.TaskID,
		})
	}
	return id, nil
}

// =====================================================================
// UpsertOtherService
// =====================================================================

// UpsertOtherServiceReq is the input for UpsertOtherService.
type UpsertOtherServiceReq struct {
	Domain      string // domain or ip required
	IP          string
	Port        int
	ServiceName string
	Auth        []map[string]any
	TaskID      int64
}

// UpsertOtherService inserts or merges a non-HTTP service asset.
func (s *AssetStore) UpsertOtherService(req UpsertOtherServiceReq) (int64, error) {
	if req.Domain == "" && req.IP == "" {
		return 0, fmt.Errorf("domain or ip is required")
	}
	if req.Port == 0 {
		return 0, fmt.Errorf("port is required")
	}
	if req.ServiceName == "" {
		return 0, fmt.Errorf("service_name is required")
	}
	// normalize service_name (lowercase) so the (domain,ip,port,service_name)
	// dedup key doesn't split "SSH" and "ssh" into separate rows.
	serviceName := strings.ToLower(strings.TrimSpace(req.ServiceName))

	domain := DomainKey(req.Domain)
	var rootDomain string
	if domain != "" {
		rootDomain, _ = RootDomain(domain)
		if rootDomain == "" {
			rootDomain = domain
		}
	}

	cseg := calcCSegment(req.IP)
	companyID, err := s.company.ResolveCompany(rootDomain, req.IP)
	if err != nil {
		return 0, err
	}

	var taskIDs string
	if req.TaskID > 0 {
		taskIDs = fmt.Sprintf("{%d}", req.TaskID)
	} else {
		taskIDs = "{}"
	}

	authJSON, err := marshalJSONBArray(req.Auth)
	if err != nil {
		return 0, err
	}

	var domainVal, rootDomainVal, ipVal, csegVal any
	if domain != "" {
		domainVal = domain
	}
	if rootDomain != "" {
		rootDomainVal = rootDomain
	}
	if req.IP != "" {
		ipVal = req.IP
	}
	if cseg != "" {
		csegVal = cseg
	}

	var id int64
	err = s.db.QueryRow(`
INSERT INTO assets(
    type, service_type, service_name, domain, ip, port, root_domain,
    c_segment, auth, company_id, task_ids
)
VALUES ('service', 'other', $1, $2, $3, $4, $5, $6::cidr, $7::jsonb[], $8, $9::bigint[])
ON CONFLICT (COALESCE(domain,''), COALESCE(ip,''), port, service_name) WHERE type = 'service' AND service_type = 'other' DO UPDATE SET
    domain     = COALESCE(EXCLUDED.domain,     assets.domain),
    ip         = COALESCE(EXCLUDED.ip,         assets.ip),
    c_segment  = COALESCE(EXCLUDED.c_segment,  assets.c_segment),
    auth       = (
        SELECT ARRAY(
            SELECT DISTINCT ON (elem::text) elem
            FROM unnest(assets.auth || EXCLUDED.auth) AS elem
        )
    ),
    task_ids   = (SELECT ARRAY(SELECT DISTINCT unnest(assets.task_ids || EXCLUDED.task_ids))),
    company_id = COALESCE(EXCLUDED.company_id, assets.company_id),
    last_seen  = now()
RETURNING id`,
		serviceName, domainVal, ipVal, req.Port, rootDomainVal,
		csegVal, authJSON, companyID, taskIDs,
	).Scan(&id)
	if err != nil {
		return 0, err
	}

	// side effects
	if req.IP != "" && req.Port > 0 {
		var boundDomains []string
		if domain != "" {
			boundDomains = []string{domain}
		}
		_, _ = s.UpsertIP(UpsertIPReq{
			IP:           req.IP,
			BoundDomains: boundDomains,
			OpenPorts:    []PortService{{Port: req.Port, Service: serviceName}},
			TaskID:       req.TaskID,
		})
	}
	s.linkHostAssets(domain, rootDomain, req.TaskID)
	return id, nil
}

// linkHostAssets ensures a service/endpoint's host is also registered as its own
// root_domain and (when it's a real subdomain, not the apex or an IP) subdomain
// asset — so those asset types stay populated and can anchor task scope. Best-effort.
func (s *AssetStore) linkHostAssets(domain, rootDomain string, taskID int64) {
	if rootDomain != "" {
		_, _ = s.UpsertRootDomain(UpsertRootDomainReq{Domain: rootDomain, TaskID: taskID})
	}
	if domain != "" && domain != rootDomain && net.ParseIP(domain) == nil {
		_, _ = s.UpsertSubdomain(UpsertSubdomainReq{Domain: domain, TaskID: taskID})
	}
}

// =====================================================================
// UpsertEndpoint
// =====================================================================

// UpsertEndpointReq is the input for UpsertEndpoint.
type UpsertEndpointReq struct {
	URL    string
	Method string
	Params []map[string]any
	IP     string // optional
	TaskID int64
}

// UpsertEndpoint inserts or merges an endpoint asset. Domain, port, root_domain
// are auto-extracted from the URL.
func (s *AssetStore) UpsertEndpoint(req UpsertEndpointReq) (int64, error) {
	if req.URL == "" {
		return 0, fmt.Errorf("url is required")
	}
	if req.Method == "" {
		return 0, fmt.Errorf("method is required")
	}
	method := strings.ToUpper(req.Method)
	normURL := normalizeURL(req.URL)
	domain, port, _ := parseURL(normURL)
	rootDomain, _ := RootDomain(domain)
	if rootDomain == "" {
		rootDomain = domain
	}

	cseg := calcCSegment(req.IP)
	companyID, err := s.company.ResolveCompany(rootDomain, req.IP)
	if err != nil {
		return 0, err
	}

	var taskIDs string
	if req.TaskID > 0 {
		taskIDs = fmt.Sprintf("{%d}", req.TaskID)
	} else {
		taskIDs = "{}"
	}

	paramsJSON, err := marshalJSONBArray(req.Params)
	if err != nil {
		return 0, err
	}

	var domainVal, rootDomainVal, ipVal, csegVal any
	if domain != "" {
		domainVal = domain
	}
	if rootDomain != "" {
		rootDomainVal = rootDomain
	}
	if req.IP != "" {
		ipVal = req.IP
	}
	if cseg != "" {
		csegVal = cseg
	}

	var id int64
	err = s.db.QueryRow(`
INSERT INTO assets(
    type, url, method, domain, ip, port, root_domain,
    params, company_id, task_ids, c_segment
)
VALUES ('endpoint', $1, $2, $3, $4, $5, $6, $7::jsonb[], $8, $9::bigint[], $10::cidr)
ON CONFLICT (url, method) WHERE type = 'endpoint' DO UPDATE SET
    params     = (
        SELECT ARRAY(
            SELECT DISTINCT ON ((elem->>'location'), (elem->>'name')) elem
            FROM unnest(assets.params || EXCLUDED.params) AS elem
            ORDER BY (elem->>'location'), (elem->>'name'), elem::text DESC
        )
    ),
    ip         = COALESCE(EXCLUDED.ip,        assets.ip),
    c_segment  = COALESCE(EXCLUDED.c_segment, assets.c_segment),
    task_ids   = (SELECT ARRAY(SELECT DISTINCT unnest(assets.task_ids || EXCLUDED.task_ids))),
    company_id = COALESCE(EXCLUDED.company_id, assets.company_id),
    last_seen  = now()
RETURNING id`,
		normURL, method, domainVal, ipVal, nullableInt(port), rootDomainVal,
		paramsJSON, companyID, taskIDs, csegVal,
	).Scan(&id)
	if err != nil {
		return 0, err
	}
	// side effects: endpoint previously registered none — register its host as
	// root_domain + subdomain(+IP) so those asset types get populated too.
	s.linkHostAssets(domain, rootDomain, req.TaskID)
	if req.IP != "" {
		_, _ = s.UpsertIP(UpsertIPReq{IP: req.IP, TaskID: req.TaskID})
	}
	return id, nil
}

// =====================================================================
// Query helpers
// =====================================================================

// QueryByType returns assets rows of a given type, newest first.
func (s *AssetStore) QueryByType(typ string, limit, offset int) ([]*Asset, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.Query(`
SELECT id, type, company_id, array_to_json(task_ids)::text,
       COALESCE(domain,''), COALESCE(root_domain,''), COALESCE(ip,''),
       COALESCE(c_segment::text,''), port,
       COALESCE(icp,''), array_to_json(bound_domains)::text, array_to_json(open_ports)::text, COALESCE(record_type,''),
       array_to_json(record_value)::text, COALESCE(bundle_id,''), COALESCE(app_name,''),
       COALESCE(category,''), COALESCE(app_description,''), COALESCE(app_icp,''),
       COALESCE(url,''), COALESCE(service_type,''), COALESCE(service_name,''),
       COALESCE(favicon_mmh3,''), status_code, content_length,
       COALESCE(page_title,''), array_to_json(technologies)::text, array_to_json(auth)::text,
       COALESCE(method,''), array_to_json(params)::text, extra, last_seen::text
FROM assets
WHERE type = $1
ORDER BY last_seen DESC
LIMIT $2 OFFSET $3`, typ, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanAssets(rows)
}

// CountByType returns the total number of assets of a type (for server-side pagination).
func (s *AssetStore) CountByType(typ string) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT count(*) FROM assets WHERE type = $1`, typ).Scan(&n)
	return n, err
}

// QueryByCompany returns assets for a company, optionally filtered by type.
func (s *AssetStore) QueryByCompany(companyID int64, typ string, limit int) ([]*Asset, error) {
	if limit <= 0 {
		limit = 200
	}
	q := `SELECT id, type, company_id, array_to_json(task_ids)::text,
       COALESCE(domain,''), COALESCE(root_domain,''), COALESCE(ip,''),
       COALESCE(c_segment::text,''), port,
       COALESCE(icp,''), array_to_json(bound_domains)::text, array_to_json(open_ports)::text, COALESCE(record_type,''),
       array_to_json(record_value)::text, COALESCE(bundle_id,''), COALESCE(app_name,''),
       COALESCE(category,''), COALESCE(app_description,''), COALESCE(app_icp,''),
       COALESCE(url,''), COALESCE(service_type,''), COALESCE(service_name,''),
       COALESCE(favicon_mmh3,''), status_code, content_length,
       COALESCE(page_title,''), array_to_json(technologies)::text, array_to_json(auth)::text,
       COALESCE(method,''), array_to_json(params)::text, extra, last_seen::text
FROM assets WHERE company_id = $1`
	args := []any{companyID}
	if typ != "" {
		args = append(args, typ)
		q += fmt.Sprintf(` AND type = $%d`, len(args))
	}
	args = append(args, limit)
	q += fmt.Sprintf(` ORDER BY last_seen DESC LIMIT $%d`, len(args))
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanAssets(rows)
}

// QueryByTask returns assets rows that have a given task_id in task_ids.
func (s *AssetStore) QueryByTask(taskID int64, typ string, limit int) ([]*Asset, error) {
	if limit <= 0 {
		limit = 200
	}
	q := `SELECT id, type, company_id, array_to_json(task_ids)::text,
       COALESCE(domain,''), COALESCE(root_domain,''), COALESCE(ip,''),
       COALESCE(c_segment::text,''), port,
       COALESCE(icp,''), array_to_json(bound_domains)::text, array_to_json(open_ports)::text, COALESCE(record_type,''),
       array_to_json(record_value)::text, COALESCE(bundle_id,''), COALESCE(app_name,''),
       COALESCE(category,''), COALESCE(app_description,''), COALESCE(app_icp,''),
       COALESCE(url,''), COALESCE(service_type,''), COALESCE(service_name,''),
       COALESCE(favicon_mmh3,''), status_code, content_length,
       COALESCE(page_title,''), array_to_json(technologies)::text, array_to_json(auth)::text,
       COALESCE(method,''), array_to_json(params)::text, extra, last_seen::text
FROM assets WHERE $1 = ANY(task_ids)`
	args := []any{taskID}
	if typ != "" {
		args = append(args, typ)
		q += fmt.Sprintf(` AND type = $%d`, len(args))
	}
	args = append(args, limit)
	q += fmt.Sprintf(` ORDER BY last_seen DESC LIMIT $%d`, len(args))
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanAssets(rows)
}

const assetSelectCols = `SELECT id, type, company_id, array_to_json(task_ids)::text,
       COALESCE(domain,''), COALESCE(root_domain,''), COALESCE(ip,''),
       COALESCE(c_segment::text,''), port,
       COALESCE(icp,''), array_to_json(bound_domains)::text, array_to_json(open_ports)::text, COALESCE(record_type,''),
       array_to_json(record_value)::text, COALESCE(bundle_id,''), COALESCE(app_name,''),
       COALESCE(category,''), COALESCE(app_description,''), COALESCE(app_icp,''),
       COALESCE(url,''), COALESCE(service_type,''), COALESCE(service_name,''),
       COALESCE(favicon_mmh3,''), status_code, content_length,
       COALESCE(page_title,''), array_to_json(technologies)::text, array_to_json(auth)::text,
       COALESCE(method,''), array_to_json(params)::text, extra, last_seen::text
FROM assets`


// GetByIDs returns assets with the given ids (order preserved by id array order).
func (s *AssetStore) GetByIDs(ids []int64) ([]*Asset, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
		args[i] = id
	}
	sql := assetSelectCols + " WHERE id IN (" + strings.Join(placeholders, ",") + ") ORDER BY last_seen DESC"
	rows, err := s.db.Query(sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanAssets(rows)
}

// DeleteByCompanyID hard-deletes all assets belonging to a company. Returns rows deleted.
func (s *AssetStore) DeleteByCompanyID(companyID int64) (int64, error) {
	res, err := s.db.Exec(`DELETE FROM assets WHERE company_id = $1`, companyID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// DeleteByIDs hard-deletes assets by their IDs. Returns the number of rows deleted.
func (s *AssetStore) DeleteByIDs(ids []int64) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
		args[i] = id
	}
	res, err := s.db.Exec("DELETE FROM assets WHERE id IN ("+strings.Join(placeholders, ",")+")", args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// CountsByType returns asset counts per type.
func (s *AssetStore) CountsByType() (map[string]int, error) {
	rows, err := s.db.Query(`SELECT type, COUNT(*) FROM assets GROUP BY type`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var typ string
		var cnt int
		if err := rows.Scan(&typ, &cnt); err != nil {
			return nil, err
		}
		out[typ] = cnt
	}
	return out, rows.Err()
}

// scanAssets scans the wide SELECT that covers all type columns.
func scanAssets(rows *sql.Rows) ([]*Asset, error) {
	var out []*Asset
	for rows.Next() {
		a := &Asset{}
		var taskIDsRaw, boundDomainsRaw, openPortsRaw, recordValueRaw, techsRaw, authRaw, paramsRaw, extraRaw []byte
		var port, statusCode sql.NullInt64
		var contentLength sql.NullInt64
		var companyID sql.NullInt64
		if err := rows.Scan(
			&a.ID, &a.Type, &companyID, &taskIDsRaw,
			&a.Domain, &a.RootDomain, &a.IP, &a.CSegment, &port,
			&a.ICP, &boundDomainsRaw, &openPortsRaw, &a.RecordType,
			&recordValueRaw, &a.BundleID, &a.AppName,
			&a.Category, &a.AppDescription, &a.AppICP,
			&a.URL, &a.ServiceType, &a.ServiceName,
			&a.FaviconMMH3, &statusCode, &contentLength,
			&a.PageTitle, &techsRaw, &authRaw,
			&a.Method, &paramsRaw, &extraRaw, &a.LastSeen,
		); err != nil {
			return nil, err
		}
		if companyID.Valid {
			cid := companyID.Int64
			a.CompanyID = &cid
		}
		if port.Valid {
			p := int(port.Int64)
			a.Port = &p
		}
		if statusCode.Valid {
			sc := int(statusCode.Int64)
			a.StatusCode = &sc
		}
		if contentLength.Valid {
			cl := contentLength.Int64
			a.ContentLength = &cl
		}
		// parse arrays
		if len(taskIDsRaw) > 0 {
			_ = json.Unmarshal(taskIDsRaw, &a.TaskIDs)
		}
		if len(boundDomainsRaw) > 0 {
			_ = json.Unmarshal(boundDomainsRaw, &a.BoundDomains)
		}
		if len(openPortsRaw) > 0 {
			_ = json.Unmarshal(openPortsRaw, &a.OpenPorts)
		}
		if len(recordValueRaw) > 0 {
			_ = json.Unmarshal(recordValueRaw, &a.RecordValue)
		}
		if len(techsRaw) > 0 {
			_ = json.Unmarshal(techsRaw, &a.Technologies)
		}
		if len(authRaw) > 0 {
			_ = json.Unmarshal(authRaw, &a.Auth)
		}
		if len(paramsRaw) > 0 {
			_ = json.Unmarshal(paramsRaw, &a.Params)
		}
		if len(extraRaw) > 0 {
			_ = json.Unmarshal(extraRaw, &a.Extra)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
