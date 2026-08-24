package db

import (
	"database/sql"
	"errors"
	"fmt"
	"net"
	"strings"
	"unicode/utf8"
)

const (
	MaxTaskAssetMutationCount = 100
	MaxTaskAssetSummaryRunes  = 500
	defaultTaskAssetSource    = "system"
	manualTaskScopeSummary    = "用户在测试资产页手工新增"
)

var (
	ErrTaskAssetInvalid       = errors.New("invalid task asset association")
	ErrTaskAssetTaskNotFound  = errors.New("task not found")
	ErrTaskAssetAssetNotFound = errors.New("asset not found")
)

// TaskAssetMutation summarizes one attach request. Attached counts newly added
// associations; Existing counts requested assets that were already on the task.
type TaskAssetMutation struct {
	Requested int `json:"requested"`
	Attached  int `json:"attached"`
	Existing  int `json:"existing"`
}

// TaskAssetScopeMutation summarizes one free-form scope registration. Domain
// and IP entries create or reuse global assets; every entry also becomes an
// idempotent task_scope row.
type TaskAssetScopeMutation struct {
	Requested      int `json:"requested"`
	AssetsLinked   int `json:"assets_linked"`
	AssetsExisting int `json:"assets_existing"`
	ScopesAdded    int `json:"scopes_added"`
	ScopesExisting int `json:"scopes_existing"`
}

// IntentAsset describes an asset explicitly anchored to a worker intent.
type IntentAsset struct {
	IntentID      int64  `json:"intent_id"`
	AssetID       int64  `json:"asset_id"`
	Type          string `json:"type"`
	Label         string `json:"label"`
	Source        string `json:"source"`
	SourceSummary string `json:"source_summary"`
	SourceNodeID  *int64 `json:"source_node_id,omitempty"`
	SourceTaskID  int64  `json:"source_task_id"`
	Inherited     bool   `json:"inherited"`
}

func normalizeTaskAssetIDs(ids []int64) ([]int64, error) {
	if len(ids) == 0 {
		return nil, fmt.Errorf("%w: asset_ids is required", ErrTaskAssetInvalid)
	}
	seen := make(map[int64]struct{}, len(ids))
	normalized := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id <= 0 {
			return nil, fmt.Errorf("%w: asset id must be positive", ErrTaskAssetInvalid)
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		normalized = append(normalized, id)
		if len(normalized) > MaxTaskAssetMutationCount {
			return nil, fmt.Errorf("%w: at most %d assets per request", ErrTaskAssetInvalid, MaxTaskAssetMutationCount)
		}
	}
	return normalized, nil
}

func normalizeTaskAssetSource(source, summary string) (string, string, error) {
	source = strings.TrimSpace(strings.ToLower(source))
	if source == "" {
		source = defaultTaskAssetSource
	}
	summary = strings.TrimSpace(summary)
	if utf8.RuneCountInString(summary) > MaxTaskAssetSummaryRunes {
		return "", "", fmt.Errorf("%w: source summary exceeds %d characters", ErrTaskAssetInvalid, MaxTaskAssetSummaryRunes)
	}
	return source, summary, nil
}

// SetTaskAssetSource improves the generic trigger-created provenance for one
// existing task association. It never creates or deletes an asset.
func (s *AssetStore) SetTaskAssetSource(taskID, assetID int64, source, summary string, sourceNodeID *int64) error {
	if taskID <= 0 || assetID <= 0 {
		return fmt.Errorf("%w: task and asset ids must be positive", ErrTaskAssetInvalid)
	}
	source, summary, err := normalizeTaskAssetSource(source, summary)
	if err != nil {
		return err
	}
	query := `
INSERT INTO task_asset_links(task_id, asset_id, source, source_summary, source_node_id)
SELECT task.id, asset.id, $3, $4, $5
FROM tasks task
JOIN assets asset ON asset.id=$2 AND task.id=ANY(asset.task_ids)
WHERE task.id=$1 AND task.deleted_at IS NULL
ON CONFLICT (task_id, asset_id) DO UPDATE
SET source=EXCLUDED.source,
    source_summary=EXCLUDED.source_summary,
    source_node_id=COALESCE(EXCLUDED.source_node_id, task_asset_links.source_node_id)`
	var result sql.Result
	if s.tx != nil {
		result, err = s.tx.Exec(query, taskID, assetID, source, summary, sourceNodeID)
	} else {
		result, err = s.db.Exec(query, taskID, assetID, source, summary, sourceNodeID)
	}
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return fmt.Errorf("%w: task or asset association does not exist", ErrTaskAssetInvalid)
	}
	return nil
}

// RegisterTaskAssetScopes accepts the same structured scope rules as enterprise
// assets. The entire request is atomic: invalid input or any storage failure
// leaves both global assets and task scope unchanged.
func (s *AssetStore) RegisterTaskAssetScopes(taskID int64, inputs []ScopeInput) (TaskAssetScopeMutation, error) {
	mutation := TaskAssetScopeMutation{Requested: len(inputs)}
	if taskID <= 0 {
		return mutation, fmt.Errorf("%w: task id must be positive", ErrTaskAssetInvalid)
	}
	if len(inputs) == 0 {
		return mutation, fmt.Errorf("%w: scope is required", ErrTaskAssetInvalid)
	}
	if err := ValidateCompanyScopeInputBounds(inputs); err != nil {
		return mutation, fmt.Errorf("%w: %v", ErrTaskAssetInvalid, err)
	}
	parsed := make([]ParsedScope, 0, len(inputs))
	for index, input := range inputs {
		rule, err := ParseScopeInput(input)
		if err != nil {
			return mutation, fmt.Errorf("%w: 第 %d 条范围无效: %v", ErrTaskAssetInvalid, index+1, err)
		}
		parsed = append(parsed, rule)
	}
	if err := validateParsedScopeBounds(parsed); err != nil {
		return mutation, fmt.Errorf("%w: %v", ErrTaskAssetInvalid, err)
	}

	tx, err := s.db.Begin()
	if err != nil {
		return mutation, err
	}
	defer tx.Rollback() //nolint:errcheck
	if err := lockCompanyScopeMutation(tx); err != nil {
		return mutation, err
	}
	var taskExists bool
	if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM tasks WHERE id=$1 AND deleted_at IS NULL)`, taskID).Scan(&taskExists); err != nil {
		return mutation, err
	}
	if !taskExists {
		return mutation, ErrTaskAssetTaskNotFound
	}

	scoped := &AssetStore{db: s.db, company: s.company, tx: tx}
	for _, rule := range parsed {
		taskScope := TaskScope{
			TaskID: taskID,
			Source: "manual",
			Reason: manualTaskScopeSummary,
		}
		var assetID int64
		switch rule.Kind {
		case "domain":
			taskScope.Kind = "root_domain"
			taskScope.Domain = rule.Domain
			var alreadyLinked bool
			err := tx.QueryRow(`SELECT id, $2=ANY(task_ids) FROM assets WHERE type='root_domain' AND domain=$1`, rule.Domain, taskID).
				Scan(&assetID, &alreadyLinked)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return mutation, err
			}
			assetID, err = scoped.UpsertRootDomain(UpsertRootDomainReq{Domain: rule.Domain, TaskID: taskID})
			if err != nil {
				return mutation, err
			}
			if alreadyLinked {
				mutation.AssetsExisting++
			} else {
				mutation.AssetsLinked++
			}
		case "ip":
			taskScope.Kind = "ip"
			taskScope.Net = rule.Net
			ip, _, parseErr := net.ParseCIDR(rule.Net)
			if parseErr != nil {
				return mutation, fmt.Errorf("%w: 无效 IP: %s", ErrTaskAssetInvalid, rule.Raw)
			}
			ipValue := ip.String()
			var alreadyLinked bool
			err := tx.QueryRow(`SELECT id, $2=ANY(task_ids) FROM assets WHERE type='ip' AND ip=$1`, ipValue, taskID).
				Scan(&assetID, &alreadyLinked)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return mutation, err
			}
			assetID, err = scoped.UpsertIP(UpsertIPReq{IP: ipValue, TaskID: taskID})
			if err != nil {
				return mutation, err
			}
			if alreadyLinked {
				mutation.AssetsExisting++
			} else {
				mutation.AssetsLinked++
			}
		case "cidr":
			taskScope.Kind = "cidr"
			taskScope.Net = rule.Net
		case "icp", "keyword":
			taskScope.Kind = rule.Kind
			taskScope.Value = rule.Value
		default:
			return mutation, fmt.Errorf("%w: unsupported scope kind %q", ErrTaskAssetInvalid, rule.Kind)
		}

		if assetID > 0 {
			if err := scoped.SetTaskAssetSource(taskID, assetID, "manual", manualTaskScopeSummary, nil); err != nil {
				return mutation, err
			}
		}
		inserted, err := scoped.upsertTaskScopeResult(taskScope)
		if err != nil {
			return mutation, err
		}
		if inserted {
			mutation.ScopesAdded++
		} else {
			mutation.ScopesExisting++
		}
	}
	if err := tx.Commit(); err != nil {
		return mutation, err
	}
	return mutation, nil
}

// AttachAssetsToTask associates existing global assets with one live task and
// records an operator-authored source summary. Global asset rows are retained.
func (s *AssetStore) AttachAssetsToTask(taskID int64, assetIDs []int64, sourceSummary string) (TaskAssetMutation, error) {
	var mutation TaskAssetMutation
	assetIDs, err := normalizeTaskAssetIDs(assetIDs)
	if err != nil {
		return mutation, err
	}
	_, sourceSummary, err = normalizeTaskAssetSource("manual", sourceSummary)
	if err != nil {
		return mutation, err
	}
	if sourceSummary == "" {
		return mutation, fmt.Errorf("%w: source_summary is required", ErrTaskAssetInvalid)
	}
	mutation.Requested = len(assetIDs)
	tx, err := s.db.Begin()
	if err != nil {
		return mutation, err
	}
	defer tx.Rollback() //nolint:errcheck

	var taskExists bool
	if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM tasks WHERE id=$1 AND deleted_at IS NULL)`, taskID).Scan(&taskExists); err != nil {
		return mutation, err
	}
	if !taskExists {
		return mutation, ErrTaskAssetTaskNotFound
	}
	var found, existing int
	if err := tx.QueryRow(`
SELECT count(*), count(*) FILTER (WHERE $1=ANY(task_ids))
FROM assets WHERE id=ANY($2::bigint[])`, taskID, assetIDs).Scan(&found, &existing); err != nil {
		return mutation, err
	}
	if found != len(assetIDs) {
		return mutation, ErrTaskAssetAssetNotFound
	}
	// Order matters: this UPDATE fires trg_assets_task_links, which creates the
	// link rows with the generic source='system'. The INSERT below must stay
	// after it so the operator-authored 'manual' provenance wins; swapping the
	// two statements silently degrades every manual attach back to 'system'.
	if _, err := tx.Exec(`
UPDATE assets
SET task_ids=CASE WHEN $1=ANY(task_ids) THEN task_ids ELSE array_append(task_ids,$1) END
WHERE id=ANY($2::bigint[])`, taskID, assetIDs); err != nil {
		return mutation, err
	}
	if _, err := tx.Exec(`
INSERT INTO task_asset_links(task_id, asset_id, source, source_summary)
SELECT $1, id, 'manual', $3 FROM assets WHERE id=ANY($2::bigint[])
ON CONFLICT (task_id, asset_id) DO UPDATE
SET source='manual', source_summary=EXCLUDED.source_summary, source_node_id=NULL`,
		taskID, assetIDs, sourceSummary); err != nil {
		return mutation, err
	}
	mutation.Existing = existing
	mutation.Attached = len(assetIDs) - existing
	return mutation, tx.Commit()
}

// DetachAssetFromTask removes only the task association. The global asset and
// exploration anchors remain available for historical blackboard auditing.
func (s *AssetStore) DetachAssetFromTask(taskID, assetID int64) (bool, error) {
	if taskID <= 0 || assetID <= 0 {
		return false, fmt.Errorf("%w: task and asset ids must be positive", ErrTaskAssetInvalid)
	}
	var detachedID int64
	err := s.db.QueryRow(`
UPDATE assets SET task_ids=array_remove(task_ids,$1)
WHERE id=$2 AND $1=ANY(task_ids)
RETURNING id`, taskID, assetID).Scan(&detachedID)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return detachedID == assetID, nil
}

func (s *AssetStore) hydrateTaskAssetSources(taskID int64, assets []*Asset) error {
	if len(assets) == 0 {
		return nil
	}
	ids := make([]int64, 0, len(assets))
	byID := make(map[int64]*Asset, len(assets))
	for _, asset := range assets {
		ids = append(ids, asset.ID)
		byID[asset.ID] = asset
	}
	rows, err := s.db.Query(`
SELECT asset_id, source, source_summary, source_node_id
FROM task_asset_links
WHERE task_id=$1 AND asset_id=ANY($2::bigint[])`, taskID, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var assetID int64
		var source, summary string
		var sourceNodeID sql.NullInt64
		if err := rows.Scan(&assetID, &source, &summary, &sourceNodeID); err != nil {
			return err
		}
		if asset := byID[assetID]; asset != nil {
			asset.TaskSource = source
			asset.TaskSourceSummary = summary
			if sourceNodeID.Valid {
				id := sourceNodeID.Int64
				asset.TaskSourceNodeID = &id
			}
		}
	}
	return rows.Err()
}

// IntentAssets returns all local worker targets plus immutable targets from the
// task's direct sources. Inherited non-terminal intents remain hidden, matching
// the existing source-aware session contract.
func (s *AssetStore) IntentAssets(taskID int64) ([]IntentAsset, error) {
	rows, err := s.db.Query(`
WITH context AS (
    SELECT task.id AS task_id, task.exploration_id, false AS inherited
    FROM tasks task
    WHERE task.id=$1 AND task.deleted_at IS NULL
    UNION ALL
    SELECT source.id, source.exploration_id, true
    FROM task_relations relation
    JOIN tasks source ON source.id=relation.source_task_id AND source.deleted_at IS NULL
    WHERE relation.task_id=$1
)
SELECT intent.id, asset.id, asset.type,
       CASE asset.type
         WHEN 'root_domain' THEN COALESCE(asset.domain,'')
         WHEN 'subdomain' THEN COALESCE(asset.domain,'')
         WHEN 'ip' THEN COALESCE(asset.ip,'')
         WHEN 'app' THEN COALESCE(asset.app_name,'')
		 WHEN 'service' THEN COALESCE(NULLIF(asset.url,''), NULLIF(concat_ws(':', COALESCE(NULLIF(asset.domain,''), NULLIF(asset.ip,'')), asset.port::text),''), NULLIF(asset.service_name,''), '#' || asset.id::text)
         WHEN 'endpoint' THEN COALESCE(NULLIF(asset.url,''), '#' || asset.id::text)
         ELSE '#' || asset.id::text
       END,
       COALESCE(link.source,'anchor'),
       COALESCE(NULLIF(link.source_summary,''), '意图在黑板中锚定该资产'),
       link.source_node_id, context.task_id, context.inherited
FROM context
JOIN exploration_nodes intent ON intent.exploration_id=context.exploration_id AND intent.kind='intent'
JOIN exploration_anchors anchor ON anchor.node_id=intent.id
JOIN assets asset ON asset.id=anchor.asset_id
LEFT JOIN task_asset_links link ON link.task_id=context.task_id AND link.asset_id=asset.id
WHERE NOT context.inherited OR intent.state IN ('done','blocked','exhausted','stopped')
ORDER BY context.inherited, intent.id DESC, asset.id`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []IntentAsset{}
	for rows.Next() {
		var asset IntentAsset
		var sourceNodeID sql.NullInt64
		if err := rows.Scan(&asset.IntentID, &asset.AssetID, &asset.Type, &asset.Label,
			&asset.Source, &asset.SourceSummary, &sourceNodeID, &asset.SourceTaskID, &asset.Inherited); err != nil {
			return nil, err
		}
		if sourceNodeID.Valid {
			id := sourceNodeID.Int64
			asset.SourceNodeID = &id
		}
		out = append(out, asset)
	}
	return out, rows.Err()
}
