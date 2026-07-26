package db

import (
	"database/sql"
	"fmt"
	"net"
	"strings"
)

// =====================================================================
// 公司主体层
// =====================================================================

// Company is a row in the companies table.
type Company struct {
	ID        int64   `json:"id"`
	Name      string  `json:"name"`
	NKey      string  `json:"nkey"`
	Logo      *string `json:"logo,omitempty"`
	CreatedAt string  `json:"created_at"`
	UpdatedAt string  `json:"updated_at"`
}

// CompanyWithScope extends Company with its scope rules and asset count.
type CompanyWithScope struct {
	Company
	Scope      []ScopeRule `json:"scope"`
	AssetCount int           `json:"asset_count"`
}

// ScopeRule is one company_scope row.
type ScopeRule struct {
	ID        int64  `json:"id"`
	CompanyID int64  `json:"company_id"`
	Kind      string `json:"kind"`
	Domain    string `json:"domain,omitempty"`
	Net       string `json:"net,omitempty"`
	Raw       string `json:"raw"`
	Reason    string `json:"reason,omitempty"`
}

// CompanyStore operates on the companies + company_scope tables.
type CompanyStore struct{ db *DB }

// Companies returns the company store.
func (d *DB) Companies() *CompanyStore { return &CompanyStore{db: d} }

// companyNKey normalises a company name: lowercase + trim + collapse whitespace.
func companyNKey(name string) string {
	return strings.Join(strings.Fields(strings.ToLower(name)), " ")
}

// UpsertCompany creates or updates a company by name. Returns the id and whether
// a new row was created.
func (s *CompanyStore) UpsertCompany(name, logo string) (id int64, created bool, err error) {
	nkey := companyNKey(name)
	var logoVal any
	if logo != "" {
		logoVal = logo
	}
	err = s.db.QueryRow(`
INSERT INTO companies(name, nkey, logo)
VALUES ($1, $2, $3)
ON CONFLICT (nkey) DO UPDATE SET
    name = EXCLUDED.name,
    logo = COALESCE(EXCLUDED.logo, companies.logo),
    updated_at = now()
RETURNING id, (xmax = 0)`, name, nkey, logoVal).Scan(&id, &created)
	return
}

// GetCompany returns one company by id (nil if not found).
func (s *CompanyStore) GetCompany(id int64) (*Company, error) {
	c := &Company{}
	err := s.db.QueryRow(`
SELECT id, name, nkey, logo, created_at::text, updated_at::text
FROM companies WHERE id = $1`, id).Scan(
		&c.ID, &c.Name, &c.NKey, &c.Logo, &c.CreatedAt, &c.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return c, err
}

// GetCompanyByName returns one company by normalized name (nil if not found).
func (s *CompanyStore) GetCompanyByName(name string) (*Company, error) {
	nkey := companyNKey(name)
	c := &Company{}
	err := s.db.QueryRow(`
SELECT id, name, nkey, logo, created_at::text, updated_at::text
FROM companies WHERE nkey = $1`, nkey).Scan(
		&c.ID, &c.Name, &c.NKey, &c.Logo, &c.CreatedAt, &c.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return c, err
}

// UpsertByName creates the company if it doesn't exist, then returns its id.
func (s *CompanyStore) UpsertByName(name string) (int64, error) {
	id, _, err := s.UpsertCompany(name, "")
	return id, err
}

// DeleteCompany deletes a company (cascades to scope rules; assets.company_id → NULL via FK).
func (s *CompanyStore) DeleteCompany(id int64) error {
	_, err := s.db.Exec(`DELETE FROM companies WHERE id = $1`, id)
	return err
}

// ListCompanies returns all companies with scope and asset count.
func (s *CompanyStore) ListCompanies() ([]*CompanyWithScope, error) {
	rows, err := s.db.Query(`
SELECT c.id, c.name, c.nkey, c.logo, c.created_at::text, c.updated_at::text,
       COUNT(DISTINCT a.id) AS asset_count
FROM companies c
LEFT JOIN assets a ON a.company_id = c.id
GROUP BY c.id
ORDER BY c.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*CompanyWithScope
	for rows.Next() {
		cws := &CompanyWithScope{}
		if err := rows.Scan(&cws.ID, &cws.Name, &cws.NKey, &cws.Logo,
			&cws.CreatedAt, &cws.UpdatedAt, &cws.AssetCount); err != nil {
			return nil, err
		}
		out = append(out, cws)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// fetch scope rules for each company
	for _, cws := range out {
		cws.Scope, err = s.GetScope(cws.ID)
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// GetScope returns all scope rules for a company.
func (s *CompanyStore) GetScope(companyID int64) ([]ScopeRule, error) {
	rows, err := s.db.Query(`
SELECT id, company_id, kind,
       COALESCE(domain,''), COALESCE(net::text,''), raw, COALESCE(reason,'')
FROM company_scope
WHERE company_id = $1
ORDER BY id`, companyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ScopeRule
	for rows.Next() {
		var r ScopeRule
		if err := rows.Scan(&r.ID, &r.CompanyID, &r.Kind, &r.Domain, &r.Net, &r.Raw, &r.Reason); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// AddScope parses and inserts scope lines for a company, then reattributes assets.
// Returns counts of added, skipped, and invalid lines.
func (s *CompanyStore) AddScope(companyID int64, lines []string, reason string) (added, skipped, invalid int, errors []string) {
	for _, line := range lines {
		rule, err := ParseScopeLine(line)
		if err != nil {
			invalid++
			errors = append(errors, fmt.Sprintf("%s: %v", line, err))
			continue
		}
		inserted, err := s.insertScopeRule(companyID, rule, reason)
		if err != nil {
			invalid++
			errors = append(errors, fmt.Sprintf("%s: %v", line, err))
			continue
		}
		if inserted {
			added++
		} else {
			skipped++
		}
	}
	// attribute newly-covered assets
	if added > 0 {
		_ = s.attributeByCompany(companyID)
	}
	return
}

// insertScopeRule inserts a scope rule. Returns (inserted, error):
// inserted=false means ON CONFLICT DO NOTHING triggered (duplicate), not an error.
func (s *CompanyStore) insertScopeRule(companyID int64, rule ParsedScope, reason string) (inserted bool, err error) {
	var res interface{ RowsAffected() (int64, error) }
	if rule.Kind == "domain" {
		res, err = s.db.Exec(`
INSERT INTO company_scope(company_id, kind, domain, raw, reason)
VALUES ($1, 'domain', $2, $3, $4)
ON CONFLICT ON CONSTRAINT uq_sv2_domain DO NOTHING`,
			companyID, rule.Domain, rule.Raw, reason)
	} else {
		res, err = s.db.Exec(`
INSERT INTO company_scope(company_id, kind, net, raw, reason)
VALUES ($1, $2, $3::cidr, $4, $5)
ON CONFLICT ON CONSTRAINT uq_sv2_net DO NOTHING`,
			companyID, rule.Kind, rule.Net, rule.Raw, reason)
	}
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// RecomputeAttribution clears all company attribution on assets and rebuilds
// from scope rules. domain rules use root_domain equality; ip/cidr use INET >>= .
func (s *CompanyStore) RecomputeAttribution() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck

	// step 1: clear all attribution
	if _, err := tx.Exec(`UPDATE assets SET company_id = NULL`); err != nil {
		return err
	}

	// step 2: domain-based attribution (root_domain exact match)
	if _, err := tx.Exec(`
UPDATE assets a SET company_id = (
    SELECT cs.company_id FROM company_scope cs
    WHERE cs.kind = 'domain'
      AND a.root_domain = cs.domain
    ORDER BY length(cs.domain) DESC
    LIMIT 1
)
WHERE type IN ('root_domain','subdomain','service','endpoint')
  AND root_domain IS NOT NULL`); err != nil {
		return err
	}

	// step 3: ip/cidr attribution (only for assets not yet attributed)
	if _, err := tx.Exec(`
UPDATE assets a SET company_id = (
    SELECT cs.company_id FROM company_scope cs
    WHERE cs.kind IN ('ip','cidr')
      AND cs.net >>= a.ip::inet
    ORDER BY masklen(cs.net) DESC
    LIMIT 1
)
WHERE type IN ('ip','subdomain','service','endpoint')
  AND ip IS NOT NULL
  AND company_id IS NULL`); err != nil {
		return err
	}

	return tx.Commit()
}

// attributeByCompany reattributes only assets matching a specific company's scope.
// Cheaper than full recompute; used when a company's scope is updated.
func (s *CompanyStore) attributeByCompany(companyID int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck

	// clear this company's existing claims
	if _, err := tx.Exec(`UPDATE assets SET company_id = NULL WHERE company_id = $1`, companyID); err != nil {
		return err
	}

	// domain rules
	if _, err := tx.Exec(`
UPDATE assets a SET company_id = $1
FROM company_scope cs
WHERE cs.company_id = $1
  AND cs.kind = 'domain'
  AND a.root_domain = cs.domain
  AND type IN ('root_domain','subdomain','service','endpoint')
  AND a.company_id IS NULL`, companyID); err != nil {
		return err
	}

	// ip/cidr rules
	if _, err := tx.Exec(`
UPDATE assets a SET company_id = $1
FROM company_scope cs
WHERE cs.company_id = $1
  AND cs.kind IN ('ip','cidr')
  AND cs.net >>= a.ip::inet
  AND type IN ('ip','subdomain','service','endpoint')
  AND a.company_id IS NULL`, companyID); err != nil {
		return err
	}

	return tx.Commit()
}

// UpdateScope replaces all scope rules for a company and reattributes.
func (s *CompanyStore) UpdateScope(companyID int64, lines []string, reason string) (added, invalid int, errs []string) {
	// delete existing rules
	if _, err := s.db.Exec(`DELETE FROM company_scope WHERE company_id = $1`, companyID); err != nil {
		errs = append(errs, err.Error())
		return
	}
	added, _, invalid, errs = s.AddScope(companyID, lines, reason)
	return
}

// ResolveCompany returns the company_id for a given root_domain and/or ip, or nil
// if no scope rule matches. Mirrors the attribution logic used at asset insert time.
func (s *CompanyStore) ResolveCompany(rootDomain, ipStr string) (*int64, error) {
	if rootDomain != "" {
		var cid int64
		err := s.db.QueryRow(`
SELECT company_id FROM company_scope
WHERE kind = 'domain'
  AND domain = $1
ORDER BY length(domain) DESC
LIMIT 1`, rootDomain).Scan(&cid)
		if err == nil {
			return &cid, nil
		}
		if err != sql.ErrNoRows {
			return nil, err
		}
	}
	if ipStr != "" {
		if net.ParseIP(ipStr) != nil {
			var cid int64
			err := s.db.QueryRow(`
SELECT company_id FROM company_scope
WHERE kind IN ('ip','cidr')
  AND net >>= $1::inet
ORDER BY masklen(net) DESC
LIMIT 1`, ipStr).Scan(&cid)
			if err == nil {
				return &cid, nil
			}
			if err != sql.ErrNoRows {
				return nil, err
			}
		}
	}
	return nil, nil
}
