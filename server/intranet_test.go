package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/session"
)

func TestParseLsLa(t *testing.T) {
	out := `total 48
drwxr-xr-x  5 root root 4096 Sep 12 10:00 .
drwxr-xr-x 22 root root 4096 Sep  1 08:00 ..
-rw-r--r--  1 root root  220 Sep 10  2023 .bash_logout
-rw-r--r--  1 root root 3771 Sep 10  2023 .bashrc
drwxr-xr-x  2 root root 4096 Aug 30 11:11 my dir
lrwxrwxrwx  1 root root   11 Sep 12 09:59 link -> /etc/passwd
-rw-------  1 www-data www-data 123456 Sep 12 12:00 dump.sql
not a valid line at all
`
	entries := parseLsLa(out)
	if len(entries) != 5 {
		t.Fatalf("期望 5 条,实得 %d: %+v", len(entries), entries)
	}
	byName := map[string]dirEntry{}
	for _, e := range entries {
		byName[e.Name] = e
	}
	if e, ok := byName[".bashrc"]; !ok || e.IsDir || e.Size != 3771 {
		t.Errorf(".bashrc 解析错误: %+v", e)
	}
	if e, ok := byName["my dir"]; !ok || !e.IsDir || e.Size != 4096 {
		t.Errorf("带空格目录解析错误: %+v", e)
	}
	if e, ok := byName["link"]; !ok || e.IsDir || e.Size != 11 {
		t.Errorf("symlink 应去掉 -> target 且非目录: %+v", e)
	}
	if e, ok := byName["dump.sql"]; !ok || e.IsDir || e.Size != 123456 {
		t.Errorf("dump.sql 解析错误: %+v", e)
	}
	if _, ok := byName["."]; ok {
		t.Error(". 应被跳过")
	}
	if _, ok := byName[".."]; ok {
		t.Error(".. 应被跳过")
	}
}

func TestParseLsLaEmpty(t *testing.T) {
	if got := parseLsLa(""); len(got) != 0 {
		t.Fatalf("空输出应得空列表: %+v", got)
	}
	if got := parseLsLa("total 0\n"); len(got) != 0 {
		t.Fatalf("仅 total 行应得空列表: %+v", got)
	}
}

func TestSegmentOf(t *testing.T) {
	if got := segmentOf("10.0.0.5", "192.168.1.0/24"); got != "192.168.1.0/24" {
		t.Errorf("c_segment 优先: %s", got)
	}
	if got := segmentOf("10.0.0.5", ""); got != "10.0.0.0/24" {
		t.Errorf("IPv4 /24 推导: %s", got)
	}
	if got := segmentOf("not-an-ip", ""); got != "" {
		t.Errorf("非法 IP 应空: %q", got)
	}
}

func TestBuildTopology(t *testing.T) {
	assets := []*db.Asset{
		{ID: 1, IP: "10.0.0.5", CSegment: "10.0.0.0/24",
			OpenPorts: []map[string]any{{"port": float64(22), "service": "ssh"}, {"port": float64(80)}}},
		{ID: 2, IP: "10.0.1.7"},
	}
	recs := []*db.SessionRecord{
		{ID: 11, HostAssetID: 1, URL: "http://10.0.0.5/shell.php", Status: db.SessionAlive, CreatedByTask: 3},
		{ID: 12, URL: "http://10.0.9.9:8080/s.jsp", Status: db.SessionAlive, CreatedByTask: 3}, // 无资产 → 合成节点
		{ID: 13, HostAssetID: 2, URL: "http://10.0.1.7/x.php", Status: db.SessionDead, CreatedByTask: 3},
	}
	tuns := []*db.TunnelRecord{
		{ID: 21, TaskID: 3, Kind: "socks", ViaSessionID: 11, State: db.TunnelAlive},
		{ID: 22, TaskID: 3, Kind: "portfwd", ViaSessionID: 12, TargetHost: "10.0.0.8", TargetPort: 3306, State: db.TunnelAlive},
	}
	topo := buildTopology(assets, recs, tuns, "10.0.0.5")

	nodes := topo["nodes"].([]*topoNode)
	// 2 资产 + 1 合成 sess + 1 合成 pf(portfwd 目标不在台账) + platform
	if len(nodes) != 5 {
		t.Fatalf("节点数应为 5: %d", len(nodes))
	}
	byID := map[string]*topoNode{}
	for _, n := range nodes {
		byID[n.ID] = n
	}
	n5 := byID["asset-1"]
	if n5 == nil || !n5.HasSession || n5.AliveSessions != 1 || !n5.IsCallback {
		t.Errorf("10.0.0.5 节点错误: %+v", n5)
	}
	if n5.IP != "10.0.0.5" {
		t.Errorf("资产节点 ip 应 10.0.0.5: %s", n5.IP)
	}
	if len(n5.Services) != 2 {
		t.Errorf("services 应 2 条: %+v", n5.Services)
	}
	n7 := byID["asset-2"]
	if n7 == nil || !n7.HasSession || n7.AliveSessions != 0 {
		t.Errorf("dead 会话主机应 has_session=true 但 alive=0: %+v", n7)
	}
	n9 := byID["sess-12"]
	if n9 == nil || n9.IP != "10.0.9.9" || n9.Segment != "10.0.9.0/24" {
		t.Errorf("合成节点错误: %+v", n9)
	}
	// 平台自身合成节点:隧道边统一从它出发;标签用回连地址,星标复用「平台自身」图例。
	platform := byID["platform"]
	if platform == nil || !platform.IsCallback || platform.IP != "10.0.0.5" {
		t.Errorf("platform 节点错误: %+v", platform)
	}
	// portfwd 目标 10.0.0.8 不在资产台账 → 合成节点 pf-<tunnel_id>,标签 host:port。
	pf := byID["pf-22"]
	if pf == nil || pf.IP != "10.0.0.8:3306" {
		t.Errorf("pf 合成节点错误: %+v", pf)
	}

	edges := topo["edges"].([]topoEdge)
	if len(edges) != 2 {
		t.Fatalf("边数应 2: %d", len(edges))
	}
	// 边的 from/to 必须是节点 id:socks 边 platform → 经由会话的宿主节点。
	if edges[0].From != "platform" || edges[0].To != "asset-1" {
		t.Errorf("socks 边应 platform → 会话宿主节点: %+v", edges[0])
	}
	if edges[1].From != "platform" || edges[1].To != "pf-22" {
		t.Errorf("portfwd 边应 platform → pf 合成节点: %+v", edges[1])
	}

	segs := topo["segments"].([]topoSegment)
	if len(segs) != 3 { // 10.0.0.0/24, 10.0.1.0/24, 10.0.9.0/24
		t.Fatalf("网段数应 3: %+v", segs)
	}
	if segs[0].ColorKey == "" || segs[0].Name != "10.0.0.0/24" {
		t.Errorf("网段着色键/排序错误: %+v", segs)
	}

	// JSON 可序列化(页面直接消费)。
	if _, err := json.Marshal(topo); err != nil {
		t.Fatalf("拓扑 JSON 序列化失败: %v", err)
	}
}

func TestBuildTopologyEmpty(t *testing.T) {
	topo := buildTopology(nil, nil, nil, "")
	if len(topo["nodes"].([]*topoNode)) != 0 ||
		len(topo["edges"].([]topoEdge)) != 0 ||
		len(topo["segments"].([]topoSegment)) != 0 {
		t.Fatalf("空输入应得空拓扑: %+v", topo)
	}
}

// TestBuildTopologyEdgeEndpoints 拓扑不变量:每条边的 from/to 都必须落在节点
// id 集合里(G6 边指向不存在的节点画不出线);经由会话不在视图内的隧道跳过。
func TestBuildTopologyEdgeEndpoints(t *testing.T) {
	assets := []*db.Asset{{ID: 1, IP: "10.0.0.5"}}
	recs := []*db.SessionRecord{
		{ID: 11, HostAssetID: 1, URL: "http://10.0.0.5/shell.php", Status: db.SessionAlive},
	}
	tuns := []*db.TunnelRecord{
		{ID: 21, Kind: "socks", ViaSessionID: 11, State: db.TunnelAlive},
		{ID: 22, Kind: "portfwd", ViaSessionID: 11, TargetHost: "10.0.0.5", TargetPort: 3306, State: db.TunnelAlive}, // 命中资产
		{ID: 23, Kind: "portfwd", ViaSessionID: 11, TargetHost: "10.0.0.8", TargetPort: 445, State: db.TunnelAlive},  // 合成 pf 节点
		{ID: 24, Kind: "socks", ViaSessionID: 99, State: db.TunnelAlive},                                            // 会话不在视图 → 跳过
	}
	topo := buildTopology(assets, recs, tuns, "")

	ids := map[string]bool{}
	for _, n := range topo["nodes"].([]*topoNode) {
		ids[n.ID] = true
	}
	edges := topo["edges"].([]topoEdge)
	if len(edges) != 3 {
		t.Fatalf("边数应 3(会话不在视图的隧道跳过): %d", len(edges))
	}
	for _, e := range edges {
		if !ids[e.From] {
			t.Errorf("边 %d from=%q 不在节点集合", e.ID, e.From)
		}
		if !ids[e.To] {
			t.Errorf("边 %d to=%q 不在节点集合", e.ID, e.To)
		}
	}
	// portfwd 命中资产 IP 时直接连到该资产节点。
	if edges[1].To != "asset-1" {
		t.Errorf("portfwd 命中资产应连 asset-1: %+v", edges[1])
	}
}

// openIntranetTestDB 连测试库;无库时 skip(与 intranet_probe_test.go 同一套语义)。
func openIntranetTestDB(t *testing.T) *db.DB {
	t.Helper()
	dsn, _, err := db.DSN()
	if err != nil {
		t.Skipf("no database config (%v) — skipping", err)
	}
	pg, err := db.Open(dsn)
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	t.Cleanup(func() { pg.Close() })
	return pg
}

// doListItems GET 台账列表并解出 items;非 200 直接 fatal。
func doListItems(t *testing.T, h http.HandlerFunc, url string) []any {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, url, nil)
	w := httptest.NewRecorder()
	h(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET %s 状态码 = %d, body = %q", url, w.Code, w.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应 JSON 解析失败: %v (%q)", err, w.Body.String())
	}
	items, _ := body["items"].([]any)
	return items
}

// expectBadTaskID 非法 task_id 一律 400。
func expectBadTaskID(t *testing.T, h http.HandlerFunc, url string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, url, nil)
	w := httptest.NewRecorder()
	h(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("GET %s 非法 task_id 应 400: %d", url, w.Code)
	}
}

// TestIntranetListSessionsTaskFilter ?task_id= 只留本任务创建的会话;不传保持全局。
func TestIntranetListSessionsTaskFilter(t *testing.T) {
	pg := openIntranetTestDB(t)
	store := db.NewSessionStore(pg, []byte("intranet-test-master-key"))
	s := &Server{sessStore: store, sessReg: session.NewRegistry()}
	ctx := context.Background()
	mk := func(taskID int64) int64 {
		t.Helper()
		id, err := store.Create(ctx, &db.SessionRecord{
			Kind: "http_php", URL: "http://10.199.0.1/s.php", Lang: "php",
			Status: db.SessionAlive, CreatedByTask: taskID,
		})
		if err != nil {
			t.Fatalf("会话落库失败: %v", err)
		}
		t.Cleanup(func() { _ = store.Delete(context.Background(), id) })
		return id
	}
	id1 := mk(980001)
	mk(980002)

	items := doListItems(t, s.intranetListSessions, "/api/sessions?task_id=980001")
	if len(items) != 1 {
		t.Fatalf("task_id=980001 应只留 1 条: %d", len(items))
	}
	it, _ := items[0].(map[string]any)
	if it["id"].(float64) != float64(id1) || it["created_by_task"].(float64) != 980001 {
		t.Errorf("会话过滤结果错误: %+v", it)
	}

	// 不传 task_id 保持全局视图(至少包含刚建的两条),兼容旧前端。
	if all := doListItems(t, s.intranetListSessions, "/api/sessions"); len(all) < 2 {
		t.Fatalf("全局视图应至少 2 条: %d", len(all))
	}
	expectBadTaskID(t, s.intranetListSessions, "/api/sessions?task_id=abc")
}

// TestIntranetListTunnelsTaskFilter ?task_id= 走 ListByTask;不传保持全局。
func TestIntranetListTunnelsTaskFilter(t *testing.T) {
	pg := openIntranetTestDB(t)
	s := &Server{m: &Manager{pg: pg}}
	store := db.NewTunnelStore(pg)
	ctx := context.Background()
	mk := func(taskID int64) int64 {
		t.Helper()
		id, err := store.Create(ctx, &db.TunnelRecord{
			TaskID: taskID, Kind: "socks", ListenHost: "127.0.0.1", ListenPort: 11080,
			State: db.TunnelAlive,
		})
		if err != nil {
			t.Fatalf("隧道落库失败: %v", err)
		}
		t.Cleanup(func() { _ = store.Delete(context.Background(), id) })
		return id
	}
	id1 := mk(980011)
	mk(980012)

	items := doListItems(t, s.intranetListTunnels, "/api/tunnels?task_id=980011")
	if len(items) != 1 {
		t.Fatalf("task_id=980011 应只留 1 条: %d", len(items))
	}
	it, _ := items[0].(map[string]any)
	if it["id"].(float64) != float64(id1) || it["task_id"].(float64) != 980011 {
		t.Errorf("隧道过滤结果错误: %+v", it)
	}

	if all := doListItems(t, s.intranetListTunnels, "/api/tunnels"); len(all) < 2 {
		t.Fatalf("全局视图应至少 2 条: %d", len(all))
	}
	expectBadTaskID(t, s.intranetListTunnels, "/api/tunnels?task_id=-1")
}

// TestIntranetListCredentialsTaskFilter ?task_id= 走 ListByTask;不传保持全局。
func TestIntranetListCredentialsTaskFilter(t *testing.T) {
	pg := openIntranetTestDB(t)
	cs := db.NewCredentialStore(pg, []byte("intranet-test-master-key"))
	s := &Server{credStore: cs}
	ctx := context.Background()
	mk := func(taskID int64) int64 {
		t.Helper()
		id, err := cs.Create(ctx, &db.CredentialRecord{
			TaskID: taskID, Username: "admin", CredType: "password",
			Secret: "s3cret-value", Source: "test",
		})
		if err != nil {
			t.Fatalf("凭据落库失败: %v", err)
		}
		t.Cleanup(func() { _ = cs.Delete(context.Background(), id) })
		return id
	}
	id1 := mk(980021)
	mk(980022)

	items := doListItems(t, s.intranetListCredentials, "/api/credentials?task_id=980021")
	if len(items) != 1 {
		t.Fatalf("task_id=980021 应只留 1 条: %d", len(items))
	}
	it, _ := items[0].(map[string]any)
	if it["id"].(float64) != float64(id1) || it["task_id"].(float64) != 980021 {
		t.Errorf("凭据过滤结果错误: %+v", it)
	}

	if all := doListItems(t, s.intranetListCredentials, "/api/credentials"); len(all) < 2 {
		t.Fatalf("全局视图应至少 2 条: %d", len(all))
	}
	expectBadTaskID(t, s.intranetListCredentials, "/api/credentials?task_id=0")
}
