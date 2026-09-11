// Package selfupdate implements ARTEX 的页面一键更新：从 GitHub Release 拉取新版
// 二进制、校验、暂存，并在下次启动时原子换装。
//
// 整体分工（见 start.sh / start.bat）：
//
//	启动脚本  = 傻瓜守护循环，只负责"进程退出后按退出码决定是否再拉起"
//	本包      = 全部易错逻辑（下载 / SHA256 校验 / 冒烟 / 换装 / 失败回滚）
//
// 之所以把换装放在 Go 而不是脚本里，是因为 sha256 校验和冒烟测试在 sh 和 bat 上
// 要写两套（sha256sum / shasum / certutil），而这恰恰是最不能出错的一环——换上一个
// 跑不起来的二进制，守护进程会忠实地反复拉起它，用户只能上机器手工救。
//
// 一次完整升级经过三次进程启动：
//
//	① 旧版 server 收到 /api/update/apply → 下载校验 → 暂存 artex.new → exit 75
//	② 脚本重新拉起旧版 → Bootstrap 发现 artex.new → 校验+冒烟 → 换装 → exit 75
//	③ 脚本重新拉起，此时已是新版 → Bootstrap 记一次尝试 → 启动成功后清除标记
//
// 任何一步失败都退回旧版：② 校验不过就删掉暂存件继续跑旧版；③ 连续 3 次没活到
// 清除标记（起不来就崩）则自动把 artex.old 换回去。
package selfupdate

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ExitRestart 是"请守护进程重新拉起我"的退出码（EX_TEMPFAIL）。启动脚本看到它
// 就立刻重跑，不计入崩溃退避。0 表示用户正常停止（脚本退出循环），其余均视为崩溃。
const ExitRestart = 75

// maxAttempts 是换装后允许的启动尝试次数。新版每次启动都会把计数 +1，活过
// settleDelay 则清除标记；连崩 maxAttempts 次说明新版根本起不来，自动回滚。
const maxAttempts = 3

// Paths 是一次升级涉及的全部文件，统一挂在**可执行文件所在目录**下。
// 刻意不用 CWD：服务化运行时工作目录可能是 / 或任意路径，用 CWD 会让暂存件落到
// 别处，换装逻辑直接失效。
type Paths struct {
	Dir     string // 可执行文件所在目录
	Current string // 当前运行的二进制        artex      / artex.exe
	New     string // 暂存的新版本            artex.new  / artex.new.exe
	Sum     string // 新版本的 sha256（hex）  artex.new.sha256 / artex.new.exe.sha256
	Old     string // 换装前备份的旧版本      artex.old  / artex.old.exe
	Marker  string // 升级状态标记            artex.upgrade.json
}

// ResolvePaths 按当前可执行文件推导全部升级路径。
//
// Windows 上 .new/.old 也必须带 .exe 后缀，否则冒烟测试和换装后的执行都会失败，
// 所以先把后缀摘掉再拼，两个平台的命名才对称。
func ResolvePaths() (Paths, error) {
	exe, err := os.Executable()
	if err != nil {
		return Paths{}, fmt.Errorf("定位可执行文件: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	dir := filepath.Dir(exe)
	name := filepath.Base(exe)
	ext := filepath.Ext(name) // Windows 上是 ".exe"，Unix 上通常为空
	stem := strings.TrimSuffix(name, ext)

	join := func(suffix string) string { return filepath.Join(dir, stem+suffix+ext) }
	return Paths{
		Dir:     dir,
		Current: exe,
		New:     join(".new"),
		Sum:     join(".new") + ".sha256",
		Old:     join(".old"),
		Marker:  filepath.Join(dir, stem+".upgrade.json"),
	}, nil
}

// marker 记录一次换装的进度，用来在新版起不来时触发自动回滚。
type marker struct {
	From     string `json:"from"`     // 升级前的版本
	To       string `json:"to"`       // 目标版本
	Attempts int    `json:"attempts"` // 换装后已尝试启动的次数
	StagedAt int64  `json:"staged_at"`
}

func readMarker(path string) (marker, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return marker{}, false
	}
	var m marker
	if json.Unmarshal(b, &m) != nil {
		return marker{}, false
	}
	return m, true
}

func writeMarker(path string, m marker) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

// cleanStaged 清掉暂存件。换装成功、校验失败、用户取消都走它，避免残留的
// artex.new 在下次启动时被重新尝试。
func cleanStaged(p Paths) {
	_ = os.Remove(p.New)
	_ = os.Remove(p.Sum)
}

// CompareVersions 比较两个版本号，返回 -1/0/1（a<b / a==b / a>b）。
// ok=false 表示至少一边不是可比较的版本号（例如本地开发构建的 "dev" 或
// git describe 产出的 "0.3.7-2-gabc1234-dirty"），此时调用方应禁用一键更新，
// 否则会把开发中的构建"升级"成正式版、覆盖掉未提交的改动。
func CompareVersions(a, b string) (int, bool) {
	av, aok := parseVersion(a)
	bv, bok := parseVersion(b)
	if !aok || !bok {
		return 0, false
	}
	for i := range 3 {
		if av[i] != bv[i] {
			if av[i] < bv[i] {
				return -1, true
			}
			return 1, true
		}
	}
	return 0, true
}

// parseVersion 解析 "v0.3.7" / "0.3.7" 形式的版本号为 [3]int。
//
// 只接受纯净的三段式：build.sh 在非 tag 构建时用 git describe 产出
// "0.3.7-2-gabc1234" 这类带后缀的版本，它们必须被判为不可比较，而不是被当成
// 0.3.7 —— 否则开发构建会被误判为"已是最新"或被正式版覆盖。
func parseVersion(s string) ([3]int, bool) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "v")
	if s == "" {
		return [3]int{}, false
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return [3]int{}, false
	}
	var out [3]int
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return [3]int{}, false
		}
		out[i] = n
	}
	return out, true
}

// InDocker 报告进程是否跑在容器里。Docker 下换装写的是容器可写层，
// `docker compose up -d` 重建容器会退回镜像自带的版本——这是预期行为
// （那时用户本来就在拉新镜像），但前端要能据此把话说清楚。
func InDocker() bool {
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return true
	}
	b, err := os.ReadFile("/proc/1/cgroup")
	if err != nil {
		return false
	}
	s := string(b)
	return strings.Contains(s, "docker") || strings.Contains(s, "containerd")
}
