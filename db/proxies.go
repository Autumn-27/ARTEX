package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// =====================================================================
// 代理池（出口代理轮换）
// =====================================================================

// Proxy is one row of the proxies table. Connection info is stored split (not a
// full URL) so entries dedup by (protocol,host,port), the UI can mask the
// password, and logs never carry credentials. Use URL() to build the dial URL.
type Proxy struct {
	ID          int64    `json:"id"`
	Protocol    string   `json:"protocol"`
	Host        string   `json:"host"`
	Port        int      `json:"port"`
	Username    string   `json:"username,omitempty"`
	Password    string   `json:"password,omitempty"`
	Anonymity   string   `json:"anonymity,omitempty"`
	Region      string   `json:"region,omitempty"`
	Tags        []string `json:"tags"`
	Label       string   `json:"label,omitempty"`
	Source      string   `json:"source"`
	Trusted     bool     `json:"trusted"`
	Enabled     bool     `json:"enabled"`
	Healthy     bool     `json:"healthy"`
	LatencyMs   int      `json:"latency_ms"`
	LastCheckAt string   `json:"last_check_at,omitempty"`
	LastOkAt    string   `json:"last_ok_at,omitempty"`
	LastError   string   `json:"last_error,omitempty"`
	FailStreak  int      `json:"fail_streak"`
	CheckCount  int      `json:"check_count"`
	OkCount     int      `json:"ok_count"`
	CreatedAt   string   `json:"created_at,omitempty"`
	UpdatedAt   string   `json:"updated_at,omitempty"`
}

// URL builds the dial URL from the split fields. Callers use this at connect time
// only — it is never persisted or logged.
func (p *Proxy) URL() *url.URL {
	u := &url.URL{Scheme: p.Protocol, Host: net.JoinHostPort(p.Host, strconv.Itoa(p.Port))}
	if p.Username != "" || p.Password != "" {
		u.User = url.UserPassword(p.Username, p.Password)
	}
	return u
}

// Masked returns a copy safe to serialize to the frontend: the password is
// replaced with a fixed placeholder when set, so it is never sent over the wire.
func (p Proxy) Masked() Proxy {
	if p.Password != "" {
		p.Password = "********"
	}
	return p
}

// ProxyFailAutoDisable is the consecutive-failure threshold past which a proxy is
// automatically disabled, so dead free-pool nodes drop out without manual cleanup.
const ProxyFailAutoDisable = 5

// SettingProxyPoolEnabled is the settings key for the pool master switch. Defined
// here so both the server (settings UI) and the agent tool layer read one key.
const SettingProxyPoolEnabled = "proxy_pool_enabled"

var ErrProxyNotFound = errors.New("proxy not found")

// ProxyStore operates on the proxies + proxy_sources tables.
type ProxyStore struct{ db *DB }

// Proxies returns a store bound to this DB.
func (d *DB) Proxies() *ProxyStore { return &ProxyStore{db: d} }

// PoolEnabled reports whether the proxy pool master switch is on (default off).
// Read from settings so the agent tool layer can gate list_proxies without a
// dependency on the server package.
func (s *ProxyStore) PoolEnabled() bool { return s.db.GetBool(SettingProxyPoolEnabled, false) }

// ProxyFilter narrows a ListProxies / SelectForHost query. Zero value = no filter.
type ProxyFilter struct {
	Protocol    string
	Region      string
	Anonymity   string
	Tags        []string
	OnlyHealthy bool
	OnlyEnabled bool
	TrustedOnly bool
}

// listProxyCols is the shared SELECT column list. tags is read as a JSON array
// text (mirrors db/assets.go's array_to_json convention) rather than a driver
// array type, so no lib/pq dependency is needed.
const listProxyCols = `id, protocol, host, port, username, password,
    anonymity, region, array_to_json(tags)::text, label, source, trusted,
    enabled, healthy, latency_ms,
    COALESCE(last_check_at::text,''), COALESCE(last_ok_at::text,''), last_error,
    fail_streak, check_count, ok_count,
    created_at::text, updated_at::text`

func scanProxy(rows *sql.Rows) (*Proxy, error) {
	var p Proxy
	var tagsJSON string
	if err := rows.Scan(&p.ID, &p.Protocol, &p.Host, &p.Port, &p.Username, &p.Password,
		&p.Anonymity, &p.Region, &tagsJSON, &p.Label, &p.Source, &p.Trusted,
		&p.Enabled, &p.Healthy, &p.LatencyMs,
		&p.LastCheckAt, &p.LastOkAt, &p.LastError,
		&p.FailStreak, &p.CheckCount, &p.OkCount,
		&p.CreatedAt, &p.UpdatedAt); err != nil {
		return nil, err
	}
	p.Tags = parseJSONStringArray(tagsJSON)
	return &p, nil
}

// ListProxies returns proxies matching the filter, newest first.
func (s *ProxyStore) ListProxies(f ProxyFilter) ([]*Proxy, error) {
	q := `SELECT ` + listProxyCols + ` FROM proxies`
	where, args := proxyWhere(f)
	if where != "" {
		q += " WHERE " + where
	}
	q += " ORDER BY id DESC"
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Proxy{}
	for rows.Next() {
		p, err := scanProxy(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// proxyWhere builds the shared WHERE clause + args ($1-based) from a filter.
func proxyWhere(f ProxyFilter) (string, []any) {
	var conds []string
	var args []any
	add := func(cond string, val any) {
		args = append(args, val)
		conds = append(conds, fmt.Sprintf(cond, len(args)))
	}
	if f.Protocol != "" {
		add("protocol = $%d", f.Protocol)
	}
	if f.Region != "" {
		add("region = $%d", f.Region)
	}
	if f.Anonymity != "" {
		add("anonymity = $%d", f.Anonymity)
	}
	if len(f.Tags) > 0 {
		add("tags && $%d", marshalStringArray(f.Tags))
	}
	if f.OnlyHealthy {
		conds = append(conds, "healthy")
	}
	if f.OnlyEnabled {
		conds = append(conds, "enabled")
	}
	if f.TrustedOnly {
		conds = append(conds, "trusted")
	}
	return strings.Join(conds, " AND "), args
}

// GetProxy loads one proxy by id.
func (s *ProxyStore) GetProxy(id int64) (*Proxy, error) {
	rows, err := s.db.Query(`SELECT `+listProxyCols+` FROM proxies WHERE id = $1`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return nil, ErrProxyNotFound
	}
	return scanProxy(rows)
}

// CreateProxy inserts a manually-entered proxy and returns its id. Duplicate
// (protocol,host,port) returns the existing row's id (idempotent).
func (s *ProxyStore) CreateProxy(p *Proxy) (int64, error) {
	if p.Protocol == "" {
		p.Protocol = "http"
	}
	if p.Host == "" || p.Port <= 0 {
		return 0, fmt.Errorf("代理需要 host 和 port")
	}
	if p.Source == "" {
		p.Source = "manual"
	}
	var id int64
	err := s.db.QueryRow(`
INSERT INTO proxies(protocol, host, port, username, password, anonymity, region, tags, label, source, trusted, enabled)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
ON CONFLICT (protocol, host, port) DO UPDATE SET updated_at = now()
RETURNING id`,
		p.Protocol, p.Host, p.Port, p.Username, p.Password, p.Anonymity, p.Region,
		marshalStringArray(p.Tags), p.Label, p.Source, p.Trusted, p.Enabled).Scan(&id)
	return id, err
}

// UpdateProxy updates the user-editable fields of one proxy (not health/quality).
func (s *ProxyStore) UpdateProxy(p *Proxy) error {
	res, err := s.db.Exec(`
UPDATE proxies SET protocol=$2, host=$3, port=$4, username=$5, password=$6,
    anonymity=$7, region=$8, tags=$9, label=$10, trusted=$11, enabled=$12
WHERE id=$1`,
		p.ID, p.Protocol, p.Host, p.Port, p.Username, p.Password,
		p.Anonymity, p.Region, marshalStringArray(p.Tags), p.Label, p.Trusted, p.Enabled)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrProxyNotFound
	}
	return nil
}

// DeleteProxy removes one proxy by id.
func (s *ProxyStore) DeleteProxy(id int64) error {
	res, err := s.db.Exec(`DELETE FROM proxies WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrProxyNotFound
	}
	return nil
}

// SetProxyEnabled toggles a proxy's user enable switch.
func (s *ProxyStore) SetProxyEnabled(id int64, enabled bool) error {
	res, err := s.db.Exec(`UPDATE proxies SET enabled=$2 WHERE id=$1`, id, enabled)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrProxyNotFound
	}
	return nil
}

// ImportProxies parses a batch of proxy lines (scheme://[user:pass@]host:port or
// bare host:port, one per line) and inserts them as trusted, source='import'.
// Returns how many new rows were added (duplicates by (protocol,host,port) are
// skipped). Unparseable lines are collected and returned for user feedback.
func (s *ProxyStore) ImportProxies(lines []string) (added int, invalid []string, err error) {
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		p, perr := ParseProxyLine(line)
		if perr != nil {
			invalid = append(invalid, line)
			continue
		}
		var id int64
		e := s.db.QueryRow(`
INSERT INTO proxies(protocol, host, port, username, password, source, trusted, enabled)
VALUES ($1,$2,$3,$4,$5,'import',true,true)
ON CONFLICT (protocol, host, port) DO NOTHING
RETURNING id`, p.Protocol, p.Host, p.Port, p.Username, p.Password).Scan(&id)
		switch {
		case e == sql.ErrNoRows: // duplicate, skipped
		case e != nil:
			return added, invalid, e
		default:
			added++
		}
	}
	return added, invalid, nil
}

// UpsertFromSource inserts proxies fetched from a free source as untrusted,
// source=<sourceName>. Existing (protocol,host,port) rows are left untouched
// (a manually-added trusted entry is never downgraded). Returns rows added.
func (s *ProxyStore) UpsertFromSource(sourceName string, proxies []Proxy) (int, error) {
	added := 0
	for i := range proxies {
		p := proxies[i]
		if p.Protocol == "" || p.Host == "" || p.Port <= 0 {
			continue
		}
		var id int64
		e := s.db.QueryRow(`
INSERT INTO proxies(protocol, host, port, anonymity, region, source, trusted, enabled)
VALUES ($1,$2,$3,$4,$5,$6,false,true)
ON CONFLICT (protocol, host, port) DO NOTHING
RETURNING id`, p.Protocol, p.Host, p.Port, p.Anonymity, p.Region, sourceName).Scan(&id)
		switch {
		case e == sql.ErrNoRows:
		case e != nil:
			return added, e
		default:
			added++
		}
	}
	return added, nil
}

// SelectForHost picks a proxy for a target host with per-host stickiness: the same
// host always maps to the same egress (until the healthy set changes), so a scan's
// requests to one target share an exit IP. Candidates are enabled+healthy (and
// trusted when trustedOnly), ranked by quality (low fail_streak, high success
// rate, recent success); a stable index derived from the host name picks within
// the ranked set. Returns nil when no candidate exists (caller falls back direct).
func (s *ProxyStore) SelectForHost(host string, trustedOnly bool) (*Proxy, error) {
	pool, err := s.ListProxies(ProxyFilter{OnlyEnabled: true, OnlyHealthy: true, TrustedOnly: trustedOnly})
	if err != nil {
		return nil, err
	}
	if len(pool) == 0 {
		return nil, nil
	}
	// Quality order: fewer consecutive fails, higher success rate, more recent success.
	sort.SliceStable(pool, func(i, j int) bool {
		a, b := pool[i], pool[j]
		if a.FailStreak != b.FailStreak {
			return a.FailStreak < b.FailStreak
		}
		ra, rb := successRate(a), successRate(b)
		if ra != rb {
			return ra > rb
		}
		return a.LastOkAt > b.LastOkAt
	})
	// Stable per-host index into the ranked set → same host, same exit.
	h := fnv.New32a()
	_, _ = h.Write([]byte(strings.ToLower(host)))
	return pool[int(h.Sum32())%len(pool)], nil
}

func successRate(p *Proxy) float64 {
	if p.CheckCount == 0 {
		return 0
	}
	return float64(p.OkCount) / float64(p.CheckCount)
}

// UpdateHealth records the outcome of one probe. On success it clears the fail
// streak and stamps last_ok_at; on failure it increments the streak and, past
// ProxyFailAutoDisable, auto-disables the proxy so dead nodes drop out.
func (s *ProxyStore) UpdateHealth(id int64, ok bool, latencyMs int, probeErr string) error {
	if ok {
		_, err := s.db.Exec(`
UPDATE proxies SET healthy=true, latency_ms=$2, last_error='',
    last_check_at=now(), last_ok_at=now(), fail_streak=0,
    check_count=check_count+1, ok_count=ok_count+1
WHERE id=$1`, id, latencyMs)
		return err
	}
	_, err := s.db.Exec(`
UPDATE proxies SET healthy=false, last_error=$2, last_check_at=now(),
    fail_streak=fail_streak+1, check_count=check_count+1,
    enabled = CASE WHEN fail_streak+1 >= $3 THEN false ELSE enabled END
WHERE id=$1`, id, probeErr, ProxyFailAutoDisable)
	return err
}

// ProxySource is one free-pool source's enable switch + last fetch status.
type ProxySource struct {
	Name        string `json:"name"`
	Enabled     bool   `json:"enabled"`
	LastFetchAt string `json:"last_fetch_at,omitempty"`
	LastCount   int    `json:"last_count"`
	LastError   string `json:"last_error,omitempty"`
}

// ListSources returns the persisted state of every source name passed in, filling
// defaults (disabled, never fetched) for names with no row yet. This keeps the
// code-defined source catalog as the source of truth for which sources exist.
func (s *ProxyStore) ListSources(names []string) ([]ProxySource, error) {
	rows, err := s.db.Query(`SELECT name, enabled, COALESCE(last_fetch_at::text,''), last_count, last_error FROM proxy_sources`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byName := map[string]ProxySource{}
	for rows.Next() {
		var src ProxySource
		if err := rows.Scan(&src.Name, &src.Enabled, &src.LastFetchAt, &src.LastCount, &src.LastError); err != nil {
			return nil, err
		}
		byName[src.Name] = src
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]ProxySource, 0, len(names))
	for _, name := range names {
		if src, ok := byName[name]; ok {
			out = append(out, src)
		} else {
			out = append(out, ProxySource{Name: name})
		}
	}
	return out, nil
}

// SetSourceEnabled toggles one free-pool source (upserts the row).
func (s *ProxyStore) SetSourceEnabled(name string, enabled bool) error {
	_, err := s.db.Exec(`
INSERT INTO proxy_sources(name, enabled) VALUES ($1,$2)
ON CONFLICT (name) DO UPDATE SET enabled = EXCLUDED.enabled`, name, enabled)
	return err
}

// EnabledSources returns the names of sources currently switched on.
func (s *ProxyStore) EnabledSources() ([]string, error) {
	rows, err := s.db.Query(`SELECT name FROM proxy_sources WHERE enabled`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	return out, rows.Err()
}

// RecordFetch stamps a source's last fetch outcome.
func (s *ProxyStore) RecordFetch(name string, count int, fetchErr string) error {
	_, err := s.db.Exec(`
INSERT INTO proxy_sources(name, enabled, last_fetch_at, last_count, last_error)
VALUES ($1, true, now(), $2, $3)
ON CONFLICT (name) DO UPDATE SET last_fetch_at = now(), last_count = $2, last_error = $3`,
		name, count, fetchErr)
	return err
}

// ParseProxyLine parses "scheme://[user:pass@]host:port" or a bare "host:port"
// (defaulting to http) into a Proxy's connection fields.
func ParseProxyLine(line string) (Proxy, error) {
	line = strings.TrimSpace(line)
	if line == "" {
		return Proxy{}, fmt.Errorf("空行")
	}
	if !strings.Contains(line, "://") {
		line = "http://" + line
	}
	u, err := url.Parse(line)
	if err != nil {
		return Proxy{}, err
	}
	scheme := strings.ToLower(u.Scheme)
	switch scheme {
	case "http", "https", "socks5", "socks4":
	default:
		return Proxy{}, fmt.Errorf("不支持的协议: %s", u.Scheme)
	}
	host := u.Hostname()
	portStr := u.Port()
	if host == "" || portStr == "" {
		return Proxy{}, fmt.Errorf("需要 host:port")
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port <= 0 || port > 65535 {
		return Proxy{}, fmt.Errorf("无效端口: %s", portStr)
	}
	p := Proxy{Protocol: scheme, Host: host, Port: port}
	if u.User != nil {
		p.Username = u.User.Username()
		p.Password, _ = u.User.Password()
	}
	return p, nil
}

// parseJSONStringArray unmarshals an array_to_json text into a []string, matching
// how db/assets.go reads TEXT[] columns. Always returns non-nil for a valid array.
func parseJSONStringArray(jsonText string) []string {
	out := []string{}
	if jsonText == "" || jsonText == "null" {
		return out
	}
	_ = json.Unmarshal([]byte(jsonText), &out)
	return out
}
