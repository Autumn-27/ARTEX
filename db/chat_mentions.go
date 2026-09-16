package db

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ChatMention is a lightweight search result. Details are read again on send.
type ChatMention struct {
	Kind        string `json:"kind"`
	ID          int64  `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description"`
}

type ChatMentionPage struct {
	Items      []ChatMention `json:"items"`
	NextCursor string        `json:"next_cursor,omitempty"`
}

var ErrInvalidChatMentionCursor = errors.New("分页位置无效，请重新搜索")

type chatMentionCursor struct {
	ID    int64  `json:"id"`
	Kind  string `json:"kind"`
	Exact bool   `json:"exact"`
	Scope string `json:"scope"`
	Query string `json:"query"`
}

func ValidChatMentionKind(kind string) bool {
	switch kind {
	case "finding", "company", "asset", "endpoint", "ip", "app", "root_domain", "subdomain", "service":
		return true
	}
	return false
}

// SearchChatMentions searches the shared catalog, just like the asset/finding
// pages. Values remain SQL parameters; %, _ and backslash are literal search text.
func (d *DB) SearchChatMentions(ctx context.Context, kind, query string) ([]ChatMention, error) {
	page, err := d.SearchChatMentionsPage(ctx, kind, query, "")
	return page.Items, err
}

// SearchChatMentionsPage uses the last result's stable sort key rather than an
// offset, so loading later pages does not repeatedly skip all earlier results.
func (d *DB) SearchChatMentionsPage(ctx context.Context, kind, query, cursor string) (ChatMentionPage, error) {
	page := ChatMentionPage{Items: make([]ChatMention, 0)}
	if kind != "" && !ValidChatMentionKind(kind) {
		return page, fmt.Errorf("不支持的引用类型")
	}
	var after chatMentionCursor
	if cursor != "" {
		if len(cursor) > 2048 {
			return page, ErrInvalidChatMentionCursor
		}
		blob, err := base64.RawURLEncoding.DecodeString(cursor)
		if err != nil || json.Unmarshal(blob, &after) != nil || after.ID <= 0 ||
			!ValidChatMentionKind(after.Kind) || after.Scope != kind || after.Query != query {
			return page, ErrInvalidChatMentionCursor
		}
	}
	pattern := "%" + strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(query) + "%"
	rows, err := d.QueryContext(ctx, `
SELECT kind, id, left(label, 160), left(description, 240) FROM (
 (SELECT 'finding' AS kind, id, COALESCE(NULLIF(name,''), vulnclass) AS label,
         concat_ws(' · ', severity, status, left(summary, 160)) AS description
  FROM findings WHERE ($1='' OR $1='finding') AND
    ($2='' OR id::text=$2 OR concat_ws(' ',name,vulnclass,summary) ILIKE $3)
    AND ($4::bigint=0 OR (id::text=$2)<$6 OR ((id::text=$2)=$6 AND (id<$4 OR (id=$4 AND 'finding'>$5))))
  ORDER BY (id::text=$2) DESC, id DESC LIMIT 21)
 UNION ALL
 (SELECT 'company', id, name, nkey FROM companies
  WHERE ($1='' OR $1='company') AND ($2='' OR id::text=$2 OR name ILIKE $3 OR nkey ILIKE $3)
    AND ($4::bigint=0 OR (id::text=$2)<$6 OR ((id::text=$2)=$6 AND (id<$4 OR (id=$4 AND 'company'>$5))))
  ORDER BY (id::text=$2) DESC, id DESC LIMIT 21)
 UNION ALL
 (SELECT type, id,
    CASE WHEN type='endpoint' THEN concat_ws(' ',NULLIF(method,''),url)
         ELSE COALESCE(NULLIF(app_name,''),NULLIF(url,''),NULLIF(domain,''),NULLIF(ip,''),NULLIF(bundle_id,''),'资产 #'||id::text) END,
    concat_ws(' · ',type,NULLIF(page_title,''),NULLIF(service_name,''),NULLIF(bundle_id,''),NULLIF(ip,''),port::text)
  FROM assets WHERE ($1='' OR $1='asset' OR type=$1) AND
    ($2='' OR id::text=$2 OR concat_ws(' ',domain,root_domain,ip,url,app_name,bundle_id,page_title,service_name,method) ILIKE $3)
    AND ($4::bigint=0 OR (id::text=$2)<$6 OR ((id::text=$2)=$6 AND (id<$4 OR (id=$4 AND type>$5))))
  ORDER BY (id::text=$2) DESC, id DESC LIMIT 21)
) matches ORDER BY (id::text=$2) DESC, id DESC, kind LIMIT 21`, kind, query, pattern, after.ID, after.Kind, after.Exact)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	for rows.Next() {
		var item ChatMention
		if err := rows.Scan(&item.Kind, &item.ID, &item.Label, &item.Description); err != nil {
			return page, err
		}
		page.Items = append(page.Items, item)
	}
	if err := rows.Err(); err != nil {
		return page, err
	}
	if len(page.Items) > 20 {
		page.Items = page.Items[:20]
		last := page.Items[19]
		blob, _ := json.Marshal(chatMentionCursor{last.ID, last.Kind, fmt.Sprint(last.ID) == query, kind, query})
		page.NextCursor = base64.RawURLEncoding.EncodeToString(blob)
	}
	return page, nil
}
