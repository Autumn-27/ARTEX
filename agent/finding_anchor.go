package agent

import (
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/Autumn-27/artex/db"
)

// report_finding 的资产自动锚定(纯逻辑层,可单测)。
//
// 背景:档 A 同入口合并(db/finding_merge.go)要求 (task_id, 归一化 vulnclass,
// 主资产=AssetIDs[0]) 三者一致;压测发现 worker 经常不带 asset_ids 上报,
// 合并键整体失效。这里在 asset_ids 为空时从上报文本(name+summary+evidence)
// 提取第一个 URL(无 URL 则提取 host[:port]/裸 host),在全库资产(调用方
// 已在 SQL 层按 host 粗筛,见 db.AssetStore.QueryByHost)中按
//
//	endpoint(同 scheme://host:port,path 前缀匹配取最长前缀)
//	→ service(同 scheme://host:port)
//	→ ip / root_domain
//
// 的优先级锚定一个主资产;全部不命中保持为空,不强行新建资产。

var (
	// findingURLRe 取文本中第一个 http(s) URL。
	findingURLRe = regexp.MustCompile(`https?://[^\s)\]"']+`)
	// findingHostRe 在无 URL 时取第一个 host[:port](IPv4 或域名)。
	findingHostRe = regexp.MustCompile(`\b(?:\d{1,3}(?:\.\d{1,3}){3}|(?:[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?\.)+[a-zA-Z]{2,})(?::\d{1,5})?\b`)
)

// anchorTarget 是从上报文本提取出的定位目标。
type anchorTarget struct {
	raw    string // 命中的原始文本(URL 或 host[:port]),用于空 name 兜底
	hasURL bool
	scheme string // 仅 hasURL 时有意义,http/https 小写
	host   string // 小写 hostname
	port   int    // 0 = 未指定;URL 时按 scheme 补默认端口
	path   string // 仅 hasURL 时有意义,至少 "/"
}

// extractAnchorTarget 取文本中第一个 URL;无 URL 时取第一个 host[:port]/裸 host。
func extractAnchorTarget(text string) anchorTarget {
	if m := findingURLRe.FindString(text); m != "" {
		m = strings.TrimRight(m, ".,;:!?")
		if u, err := url.Parse(m); err == nil && u.Hostname() != "" {
			scheme := strings.ToLower(u.Scheme)
			port := 0
			if p := u.Port(); p != "" {
				port, _ = strconv.Atoi(p)
			}
			if port == 0 {
				switch scheme {
				case "http":
					port = 80
				case "https":
					port = 443
				}
			}
			path := u.Path
			if path == "" {
				path = "/"
			}
			return anchorTarget{raw: m, hasURL: true, scheme: scheme, host: strings.ToLower(u.Hostname()), port: port, path: path}
		}
	}
	if m := findingHostRe.FindString(text); m != "" {
		host, port := m, 0
		if i := strings.LastIndex(m, ":"); i >= 0 {
			if p, err := strconv.Atoi(m[i+1:]); err == nil {
				host, port = m[:i], p
			}
		}
		return anchorTarget{raw: m, host: strings.ToLower(host), port: port, path: "/"}
	}
	return anchorTarget{}
}

// urlKey 把资产 URL 解析为 (scheme, host, port, path);host 小写、端口缺省按
// scheme 补 80/443、path 缺省为 "/"。不可解析时 ok=false。
func urlKey(raw string) (scheme, host string, port int, path string, ok bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return "", "", 0, "", false
	}
	scheme = strings.ToLower(u.Scheme)
	host = strings.ToLower(u.Hostname())
	if p := u.Port(); p != "" {
		port, _ = strconv.Atoi(p)
	}
	if port == 0 {
		switch scheme {
		case "http":
			port = 80
		case "https":
			port = 443
		}
	}
	path = u.Path
	if path == "" {
		path = "/"
	}
	return scheme, host, port, path, true
}

// pathPrefix 判断 endpoint 资产路径 prefix 是否是 finding 路径 path 的前缀
// (按路径段边界:"/api" 命中 "/api/users",但不命中 "/apisix")。
func pathPrefix(prefix, path string) bool {
	if prefix == "" || prefix == "/" {
		return true
	}
	if path == prefix {
		return true
	}
	if strings.HasSuffix(prefix, "/") {
		return strings.HasPrefix(path, prefix)
	}
	return strings.HasPrefix(path, prefix+"/")
}

// anchorFindingAsset 在 candidates(全库资产,已按 host 粗筛)中为上报文本锚定
// 一个主资产,返回资产 id 与命中理由;不命中返回 0。命中优先级见文件头注释;
// 同分时取 id 最小者,保证同一文本+同一资产集结果确定(合并键稳定)。
func anchorFindingAsset(text string, candidates []*db.Asset) (int64, string) {
	tgt := extractAnchorTarget(text)
	if tgt.host == "" {
		return 0, ""
	}
	// 1) endpoint:同 scheme://host:port 且 path 前缀匹配,取最长前缀。
	if tgt.hasURL {
		var best *db.Asset
		bestLen := -1
		for _, a := range candidates {
			if a == nil || a.Type != "endpoint" {
				continue
			}
			scheme, host, port, path, ok := urlKey(a.URL)
			if !ok || scheme != tgt.scheme || host != tgt.host || port != tgt.port {
				continue
			}
			if !pathPrefix(path, tgt.path) {
				continue
			}
			if len(path) > bestLen || (len(path) == bestLen && best != nil && a.ID < best.ID) {
				best, bestLen = a, len(path)
			}
		}
		if best != nil {
			return best.ID, "endpoint 最长前缀匹配"
		}
	}
	// 2) service:同 scheme://host:port;无 URL(裸 host[:port])时按 host(+port)匹配。
	var svc *db.Asset
	for _, a := range candidates {
		if a == nil || a.Type != "service" {
			continue
		}
		scheme, host, port, _, ok := urlKey(a.URL)
		if !ok || host != tgt.host {
			continue
		}
		if tgt.hasURL {
			if scheme != tgt.scheme || port != tgt.port {
				continue
			}
		} else if tgt.port > 0 && port != tgt.port {
			continue
		}
		if svc == nil || a.ID < svc.ID {
			svc = a
		}
	}
	if svc != nil {
		return svc.ID, "service 同 scheme://host:port 匹配"
	}
	// 3) ip / root_domain 兜底。
	var ip *db.Asset
	for _, a := range candidates {
		if a == nil || a.Type != "ip" || a.IP != tgt.host {
			continue
		}
		if ip == nil || a.ID < ip.ID {
			ip = a
		}
	}
	if ip != nil {
		return ip.ID, "ip 资产匹配"
	}
	var root *db.Asset
	for _, a := range candidates {
		if a == nil || a.Type != "root_domain" {
			continue
		}
		d := strings.ToLower(strings.TrimSpace(a.Domain))
		if d == "" || (tgt.host != d && !strings.HasSuffix(tgt.host, "."+d)) {
			continue
		}
		if root == nil || a.ID < root.ID {
			root = a
		}
	}
	if root != nil {
		return root.ID, "root_domain 资产匹配"
	}
	return 0, ""
}

// fallbackFindingName 在 name 为空时兜底:vulnclass + 文本中第一个 URL/host;
// 两者皆空时给固定占位名,避免写入空串 name。
func fallbackFindingName(vulnClass, text string) string {
	vc := strings.TrimSpace(vulnClass)
	if tgt := extractAnchorTarget(text); tgt.raw != "" {
		return strings.TrimSpace(vc + " " + tgt.raw)
	}
	if vc != "" {
		return vc
	}
	return "未命名漏洞"
}
