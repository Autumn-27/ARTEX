package db

import "time"

// AssetInterceptRule is one row of asset_intercept_rules — a global asset
// blocklist entry. Unlike intercept_rules (which matches tool name / input),
// these match the *target asset*: an exact/fuzzy domain·ip·url, or a CIDR range.
// This layer only stores rules; the matching/enforcement logic lives elsewhere.
type AssetInterceptRule struct {
	ID        int64     `json:"id"`
	Enabled   bool      `json:"enabled"`
	Kind      string    `json:"kind"` // exact_domain|exact_ip|exact_url|fuzzy_domain|fuzzy_ip|fuzzy_url|cidr
	Pattern   string    `json:"pattern"`
	Note      string    `json:"note"`
	Builtin   bool      `json:"builtin"`
	// Action 仅用于任务级规则：'block'=拦截 'allow'=允许(白名单)。
	// 全局规则(asset_intercept_rules)不带此列，恒为空，视为拦截。
	Action    string    `json:"action,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

const assetInterceptRuleCols = `id, enabled, kind, pattern, note, builtin, created_at, updated_at`

func scanAssetInterceptRule(row interface{ Scan(...any) error }) (AssetInterceptRule, error) {
	var r AssetInterceptRule
	err := row.Scan(&r.ID, &r.Enabled, &r.Kind, &r.Pattern, &r.Note, &r.Builtin, &r.CreatedAt, &r.UpdatedAt)
	return r, err
}

// ListAssetInterceptRules returns all rules, built-ins first then newest first.
func (d *DB) ListAssetInterceptRules() ([]AssetInterceptRule, error) {
	rows, err := d.Query(`SELECT ` + assetInterceptRuleCols + ` FROM asset_intercept_rules ORDER BY builtin DESC, id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AssetInterceptRule
	for rows.Next() {
		r, err := scanAssetInterceptRule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// CreateAssetInterceptRule inserts a new user rule (builtin is always false here).
func (d *DB) CreateAssetInterceptRule(kind, pattern, note string, enabled bool) (AssetInterceptRule, error) {
	row := d.QueryRow(`
INSERT INTO asset_intercept_rules(enabled, kind, pattern, note, builtin)
VALUES ($1, $2, $3, $4, false)
RETURNING `+assetInterceptRuleCols,
		enabled, kind, pattern, note)
	return scanAssetInterceptRule(row)
}

// UpdateAssetInterceptRule replaces the editable fields of an existing rule.
func (d *DB) UpdateAssetInterceptRule(id int64, kind, pattern, note string, enabled bool) (AssetInterceptRule, error) {
	row := d.QueryRow(`
UPDATE asset_intercept_rules
   SET enabled=$2, kind=$3, pattern=$4, note=$5
WHERE id=$1
RETURNING `+assetInterceptRuleCols,
		id, enabled, kind, pattern, note)
	return scanAssetInterceptRule(row)
}

// DeleteAssetInterceptRule removes a rule (built-in rules are deletable too).
func (d *DB) DeleteAssetInterceptRule(id int64) error {
	_, err := d.Exec(`DELETE FROM asset_intercept_rules WHERE id=$1`, id)
	return err
}

// ToggleAssetInterceptRule flips the enabled state of a rule.
func (d *DB) ToggleAssetInterceptRule(id int64, enabled bool) error {
	_, err := d.Exec(`UPDATE asset_intercept_rules SET enabled=$2 WHERE id=$1`, id, enabled)
	return err
}
