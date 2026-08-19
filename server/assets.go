package server

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/Autumn-27/artex/db"
)

// assetStore returns the asset store.
func (s *Server) assetStore() *db.AssetStore {
	if s.m.pg == nil {
		return nil
	}
	return s.m.pg.Assets()
}

// companyStore returns the company store.
func (s *Server) companyStore() *db.CompanyStore {
	if s.m.pg == nil {
		return nil
	}
	return s.m.pg.Companies()
}

// =====================================================================
// GET /api/companies
// =====================================================================

func (s *Server) listCompanies(w http.ResponseWriter, r *http.Request) {
	cs := s.companyStore()
	if cs == nil {
		writeErr(w, 503, "database unavailable")
		return
	}
	companies, err := cs.ListCompanies()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, companies)
}

// =====================================================================
// POST /api/companies
// =====================================================================

func (s *Server) createCompany(w http.ResponseWriter, r *http.Request) {
	cs := s.companyStore()
	if cs == nil {
		writeErr(w, 503, "database unavailable")
		return
	}
	var req struct {
		Name  string   `json:"name"`
		Logo  string   `json:"logo"`
		Scope []string `json:"scope"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "invalid JSON: "+err.Error())
		return
	}
	if req.Name == "" {
		writeErr(w, 400, "name required")
		return
	}
	id, created, err := cs.UpsertCompany(req.Name, req.Logo)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	out := map[string]any{"id": id, "created": created}
	if len(req.Scope) > 0 {
		added, skipped, invalid, errs := cs.AddScope(id, req.Scope, "api")
		out["scope_added"] = added
		out["scope_skipped"] = skipped
		out["scope_invalid"] = invalid
		if len(errs) > 0 {
			out["scope_errors"] = errs
		}
	}
	writeJSON(w, 201, out)
}

// =====================================================================
// GET /api/companies/{id}
// =====================================================================

func (s *Server) getCompany(w http.ResponseWriter, r *http.Request) {
	cs := s.companyStore()
	if cs == nil {
		writeErr(w, 503, "database unavailable")
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, 400, "invalid id")
		return
	}
	c, err := cs.GetCompany(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if c == nil {
		writeErr(w, 404, "company not found")
		return
	}
	scope, err := cs.GetScope(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"company": c, "scope": scope})
}

// =====================================================================
// POST /api/companies/{id}/scope
// =====================================================================

func (s *Server) addCompanyScope(w http.ResponseWriter, r *http.Request) {
	cs := s.companyStore()
	if cs == nil {
		writeErr(w, 503, "database unavailable")
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, 400, "invalid company id")
		return
	}
	var req struct {
		Scope  []string `json:"scope"`
		Reason string   `json:"reason"`
		Reset  bool     `json:"reset"` // if true, replace existing scope
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "invalid JSON: "+err.Error())
		return
	}
	var added, skipped, invalid int
	var errs []string
	if req.Reset {
		added, invalid, errs = cs.UpdateScope(id, req.Scope, req.Reason)
	} else {
		added, skipped, invalid, errs = cs.AddScope(id, req.Scope, req.Reason)
	}
	out := map[string]any{
		"added":   added,
		"skipped": skipped,
		"invalid": invalid,
	}
	if len(errs) > 0 {
		out["errors"] = errs
	}
	writeJSON(w, 200, out)
}

// =====================================================================
// DELETE /api/companies/{id}
// =====================================================================

func (s *Server) deleteCompany(w http.ResponseWriter, r *http.Request) {
	cs := s.companyStore()
	if cs == nil {
		writeErr(w, 503, "database unavailable")
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, 400, "invalid id")
		return
	}
	var req struct {
		DeleteAssets bool `json:"delete_assets"`
	}
	// Body is optional; ignore decode errors (e.g. empty body).
	_ = json.NewDecoder(r.Body).Decode(&req)

	assetsDeleted := int64(0)
	if req.DeleteAssets {
		as := s.assetStore()
		if as != nil {
			assetsDeleted, err = as.DeleteByCompanyID(id)
			if err != nil {
				writeErr(w, 500, err.Error())
				return
			}
		}
	}
	if err := cs.DeleteCompany(id); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"deleted": 1, "assets_deleted": assetsDeleted})
}

// =====================================================================
// POST /api/companies/reattribute
// =====================================================================

func (s *Server) reattribute(w http.ResponseWriter, r *http.Request) {
	cs := s.companyStore()
	if cs == nil {
		writeErr(w, 503, "database unavailable")
		return
	}
	if err := cs.RecomputeAttribution(); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// =====================================================================
// GET /api/assets
// =====================================================================

// defaultAssetPageSize is the page size used when the caller omits ?limit.
const defaultAssetPageSize = 50

func (s *Server) listAssets(w http.ResponseWriter, r *http.Request) {
	as := s.assetStore()
	if as == nil {
		writeErr(w, 503, "database unavailable")
		return
	}
	q := r.URL.Query()
	typ := q.Get("type")
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	// limit/offset are page controls, not a cap: `total` always reports the full
	// match count so callers can page through everything.
	if limit <= 0 {
		limit = defaultAssetPageSize
	}

	var assets []*db.Asset
	var err error
	// total is the full match count ignoring limit/offset — lets the UI paginate on
	// the server. For company/task scopes it falls back to the returned length.
	total := 0

	if dsl := q.Get("dsl"); dsl != "" {
		assets, err = as.QueryDSL(dsl, typ, limit, offset)
		if err == nil {
			total, _ = as.CountDSL(dsl, typ)
		}
	} else {
		companyID, _ := strconv.ParseInt(q.Get("company_id"), 10, 64)
		taskID, _ := strconv.ParseInt(q.Get("task_id"), 10, 64)
		switch {
		case companyID > 0:
			assets, err = as.QueryByCompany(companyID, typ, limit, offset)
			if err == nil {
				total, err = as.CountByCompany(companyID, typ)
			}
		case taskID > 0:
			assets, err = as.QueryByTask(taskID, typ, limit, offset)
			if err == nil {
				total, err = as.CountByTask(taskID, typ)
			}
		default:
			if typ == "" {
				typ = "root_domain"
			}
			assets, err = as.QueryByType(typ, limit, offset)
			if err == nil {
				total, _ = as.CountByType(typ)
			}
		}
	}

	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{
		"count":  len(assets),
		"total":  total,
		"assets": assets,
	})
}

// =====================================================================
// GET /api/assets/counts
// =====================================================================

func (s *Server) assetCounts(w http.ResponseWriter, r *http.Request) {
	as := s.assetStore()
	if as == nil {
		writeErr(w, 503, "database unavailable")
		return
	}
	var counts map[string]int
	var err error
	if taskID, _ := strconv.ParseInt(r.URL.Query().Get("task_id"), 10, 64); taskID > 0 {
		counts, err = as.CountsByTypeForTask(taskID)
	} else {
		counts, err = as.CountsByType()
	}
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, counts)
}

// =====================================================================
// DELETE /api/assets
// =====================================================================

func (s *Server) deleteAssets(w http.ResponseWriter, r *http.Request) {
	as := s.assetStore()
	if as == nil {
		writeErr(w, 503, "database unavailable")
		return
	}
	var req struct {
		IDs []int64 `json:"ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "invalid JSON: "+err.Error())
		return
	}
	if len(req.IDs) == 0 {
		writeErr(w, 400, "ids required")
		return
	}
	deleted, err := as.DeleteByIDs(req.IDs)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"deleted": deleted})
}

// =====================================================================
// POST /api/assets
// =====================================================================

func (s *Server) insertAssets(w http.ResponseWriter, r *http.Request) {
	as := s.assetStore()
	if as == nil {
		writeErr(w, 503, "database unavailable")
		return
	}
	var req struct {
		TaskID int64 `json:"task_id"`
		Assets []struct {
			Type string `json:"type"`
			// root_domain / subdomain
			Domain      string `json:"domain"`
			ICP         string `json:"icp"`
			RecordType  string   `json:"record_type"`
			RecordValue []string `json:"record_value"`
			// ip
			IP           string           `json:"ip"`
			BoundDomains []string         `json:"bound_domains"`
			OpenPorts    []db.PortService `json:"open_ports"`
			// app
			AppName     string `json:"app_name"`
			BundleID    string `json:"bundle_id"`
			Category    string `json:"category"`
			Description string `json:"description"`
			AppICP      string `json:"app_icp"`
			// service http
			URL           string           `json:"url"`
			Technologies  []string         `json:"technologies"`
			StatusCode    *int             `json:"status_code"`
			ContentLength *int64           `json:"content_length"`
			PageTitle     string           `json:"page_title"`
			FaviconMMH3   string           `json:"favicon_mmh3"`
			Auth          []map[string]any `json:"auth"`
			ServiceIP     string           `json:"service_ip"`
			// service other
			Port        int    `json:"port"`
			ServiceName string `json:"service_name"`
			// endpoint
			Method string           `json:"method"`
			Params []map[string]any `json:"params"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "invalid JSON: "+err.Error())
		return
	}

	type result struct {
		Index int    `json:"index"`
		ID    int64  `json:"id"`
		Type  string `json:"type"`
	}
	type errEntry struct {
		Index int    `json:"index"`
		Error string `json:"error"`
	}
	var results []result
	var errs []errEntry

	for i, a := range req.Assets {
		var id int64
		var err error
		switch a.Type {
		case "root_domain":
			id, err = as.UpsertRootDomain(db.UpsertRootDomainReq{Domain: a.Domain, ICP: a.ICP, TaskID: req.TaskID})
		case "ip":
			id, err = as.UpsertIP(db.UpsertIPReq{IP: a.IP, BoundDomains: a.BoundDomains, OpenPorts: a.OpenPorts, TaskID: req.TaskID})
		case "subdomain":
			id, err = as.UpsertSubdomain(db.UpsertSubdomainReq{Domain: a.Domain, RecordType: a.RecordType, RecordValue: a.RecordValue, ICP: a.ICP, TaskID: req.TaskID})
		case "app":
			id, err = as.UpsertApp(db.UpsertAppReq{Name: a.AppName, BundleID: a.BundleID, Category: a.Category, Description: a.Description, ICP: a.AppICP, TaskID: req.TaskID})
		case "service":
			if a.URL != "" {
				svcIP := a.ServiceIP
				if svcIP == "" {
					svcIP = a.IP
				}
				id, err = as.UpsertHTTPService(db.UpsertHTTPServiceReq{URL: a.URL, Technologies: a.Technologies, StatusCode: a.StatusCode, ContentLength: a.ContentLength, PageTitle: a.PageTitle, FaviconMMH3: a.FaviconMMH3, Auth: a.Auth, IP: svcIP, TaskID: req.TaskID})
			} else {
				id, err = as.UpsertOtherService(db.UpsertOtherServiceReq{Domain: a.Domain, IP: a.IP, Port: a.Port, ServiceName: a.ServiceName, Auth: a.Auth, TaskID: req.TaskID})
			}
		case "endpoint":
			svcIP := a.ServiceIP
			if svcIP == "" {
				svcIP = a.IP
			}
			id, err = as.UpsertEndpoint(db.UpsertEndpointReq{URL: a.URL, Method: a.Method, Params: a.Params, IP: svcIP, TaskID: req.TaskID})
		default:
			errs = append(errs, errEntry{Index: i, Error: "unknown type: " + a.Type})
			continue
		}
		if err != nil {
			errs = append(errs, errEntry{Index: i, Error: err.Error()})
			continue
		}
		results = append(results, result{Index: i, ID: id, Type: a.Type})
	}
	writeJSON(w, 200, map[string]any{"results": results, "errors": errs})
}
