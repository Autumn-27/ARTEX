package proxypool

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Autumn-27/artex/db"
)

// Source is one free proxy source. Only format-stable GitHub raw "host:port" lists
// are used (no HTML scraping) — each line is a bare host:port, protocol is fixed
// per source. Sources are DISABLED by default; users opt in per source.
type Source struct {
	Name     string
	URL      string
	Protocol string // protocol assigned to every entry from this source
}

// BuiltinSources is the catalog of free proxy sources. These are widely-mirrored,
// daily-updated raw text lists with a stable "ip:port\n" format. If a source dies
// (raw lists do rot), the user disables it; adding/removing entries here is the
// only maintenance touch-point.
var BuiltinSources = []Source{
	{Name: "TheSpeedX-http", URL: "https://raw.githubusercontent.com/TheSpeedX/PROXY-List/master/http.txt", Protocol: "http"},
	{Name: "TheSpeedX-socks5", URL: "https://raw.githubusercontent.com/TheSpeedX/PROXY-List/master/socks5.txt", Protocol: "socks5"},
	{Name: "TheSpeedX-socks4", URL: "https://raw.githubusercontent.com/TheSpeedX/PROXY-List/master/socks4.txt", Protocol: "socks4"},
	{Name: "monosans-http", URL: "https://raw.githubusercontent.com/monosans/proxy-list/main/proxies/http.txt", Protocol: "http"},
	{Name: "monosans-socks5", URL: "https://raw.githubusercontent.com/monosans/proxy-list/main/proxies/socks5.txt", Protocol: "socks5"},
}

// SourceNames returns every built-in source name (for seeding the source list UI).
func SourceNames() []string {
	out := make([]string, len(BuiltinSources))
	for i, s := range BuiltinSources {
		out[i] = s.Name
	}
	return out
}

// sourceByName looks up a built-in source definition.
func sourceByName(name string) (Source, bool) {
	for _, s := range BuiltinSources {
		if s.Name == name {
			return s, true
		}
	}
	return Source{}, false
}

// fetch downloads and parses one source into proxy rows (connection fields only;
// health/quality are filled later by probing).
func fetch(ctx context.Context, src Source, client *http.Client) ([]db.Proxy, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src.URL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("source status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20)) // 8MB cap
	if err != nil {
		return nil, err
	}
	return parseHostPortList(body, src.Protocol), nil
}

// parseHostPortList turns a "host:port\n" list into proxy rows. Lines that are not
// a valid host:port are skipped. Protocol is fixed per source.
func parseHostPortList(body []byte, protocol string) []db.Proxy {
	var out []db.Proxy
	sc := bufio.NewScanner(bytes.NewReader(body))
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		host, portStr, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		host = strings.TrimSpace(host)
		port, err := strconv.Atoi(strings.TrimSpace(portStr))
		if host == "" || err != nil || port <= 0 || port > 65535 {
			continue
		}
		out = append(out, db.Proxy{Protocol: protocol, Host: host, Port: port})
	}
	return out
}
