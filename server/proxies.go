package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/proxypool"
)

const maxProxyBodyBytes = 4 << 20

// maskProxies returns copies safe to serialize (password replaced with a
// placeholder), so proxy credentials never leave the backend.
func maskProxies(in []*db.Proxy) []db.Proxy {
	out := make([]db.Proxy, len(in))
	for i, p := range in {
		out[i] = p.Masked()
	}
	return out
}

// listProxies GET /api/proxies — filters ?protocol=&region=&tag=&healthy=1&enabled=1
// plus server-side paging ?page=&limit= (page 1-based; omit for all rows).
func (s *Server) listProxies(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := db.ProxyFilter{
		Protocol:    q.Get("protocol"),
		Region:      q.Get("region"),
		Anonymity:   q.Get("anonymity"),
		OnlyHealthy: q.Get("healthy") == "1",
		OnlyEnabled: q.Get("enabled") == "1",
	}
	if tag := q.Get("tag"); tag != "" {
		f.Tags = []string{tag}
	}
	page, _ := strconv.Atoi(q.Get("page"))
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit > 0 {
		if page < 1 {
			page = 1
		}
		f.Limit, f.Offset = limit, (page-1)*limit
	}
	store := s.m.Proxies()
	total, err := store.CountProxies(f)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	rows, err := store.ListProxies(f)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"proxies": maskProxies(rows), "total": total})
}

// proxyCreateReq is the create/update body. Password "" on update keeps the
// existing secret (so the masked value returned by GET is never written back).
type proxyCreateReq struct {
	Protocol  string   `json:"protocol"`
	Host      string   `json:"host"`
	Port      int      `json:"port"`
	Username  string   `json:"username"`
	Password  string   `json:"password"`
	Anonymity string   `json:"anonymity"`
	Region    string   `json:"region"`
	Tags      []string `json:"tags"`
	Label     string   `json:"label"`
	Enabled   bool     `json:"enabled"`
}

func (s *Server) decodeProxyBody(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxProxyBodyBytes)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return false
	}
	return true
}

// createProxy POST /api/proxies — add one manual proxy (trusted).
func (s *Server) createProxy(w http.ResponseWriter, r *http.Request) {
	var req proxyCreateReq
	if !s.decodeProxyBody(w, r, &req) {
		return
	}
	p := &db.Proxy{
		Protocol: req.Protocol, Host: strings.TrimSpace(req.Host), Port: req.Port,
		Username: req.Username, Password: req.Password, Anonymity: req.Anonymity,
		Region: req.Region, Tags: req.Tags, Label: req.Label,
		Source: "manual", Trusted: true, Enabled: req.Enabled,
	}
	id, err := s.m.Proxies().CreateProxy(p)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"id": id})
}

// updateProxy PUT /api/proxies/{id} — edit one proxy. A blank password preserves
// the stored secret (the UI shows a masked value it must not persist back).
func (s *Server) updateProxy(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, 400, "invalid id")
		return
	}
	var req proxyCreateReq
	if !s.decodeProxyBody(w, r, &req) {
		return
	}
	cur, err := s.m.Proxies().GetProxy(id)
	if err != nil {
		writeProxyErr(w, err)
		return
	}
	cur.Protocol, cur.Host, cur.Port = req.Protocol, strings.TrimSpace(req.Host), req.Port
	cur.Username, cur.Anonymity, cur.Region = req.Username, req.Anonymity, req.Region
	cur.Tags, cur.Label, cur.Enabled = req.Tags, req.Label, req.Enabled
	if req.Password != "" && req.Password != "********" {
		cur.Password = req.Password
	}
	if err := s.m.Proxies().UpdateProxy(cur); err != nil {
		writeProxyErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// deleteProxy DELETE /api/proxies/{id}.
func (s *Server) deleteProxy(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, 400, "invalid id")
		return
	}
	if err := s.m.Proxies().DeleteProxy(id); err != nil {
		writeProxyErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// importProxies POST /api/proxies/import — body {"text":"host:port\n..."}; adds
// each parsed line as a trusted import, de-duped by (protocol,host,port).
func (s *Server) importProxies(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Text string `json:"text"`
	}
	if !s.decodeProxyBody(w, r, &req) {
		return
	}
	lines := strings.Split(req.Text, "\n")
	added, invalid, err := s.m.Proxies().ImportProxies(lines)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"added": added, "invalid": invalid})
}

// checkProxy POST /api/proxies/{id}/check — probe one proxy on demand.
func (s *Server) checkProxy(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, 400, "invalid id")
		return
	}
	res, err := s.m.ProxyPool().ProbeNow(r.Context(), id)
	if err != nil {
		writeProxyErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{
		"ok": res.OK, "latency_ms": res.Latency.Milliseconds(), "error": res.Err,
	})
}

// listProxySources GET /api/proxy-sources — built-in source catalog + enable state.
func (s *Server) listProxySources(w http.ResponseWriter, r *http.Request) {
	sources, err := s.m.Proxies().ListSources(proxypool.SourceNames())
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"sources": sources})
}

// fetchProxySource POST /api/proxy-sources/{name}/fetch — pull one free source now.
func (s *Server) fetchProxySource(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	total, added, err := s.m.ProxyPool().FetchSourceNow(r.Context(), name)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"fetched": total, "added": added})
}

// setProxySource PUT /api/proxy-sources/{name} — body {"enabled":bool}.
func (s *Server) setProxySource(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if !s.decodeProxyBody(w, r, &req) {
		return
	}
	if err := s.m.Proxies().SetSourceEnabled(name, req.Enabled); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func writeProxyErr(w http.ResponseWriter, err error) {
	if errors.Is(err, db.ErrProxyNotFound) {
		writeErr(w, 404, "proxy not found")
		return
	}
	writeErr(w, 500, err.Error())
}
