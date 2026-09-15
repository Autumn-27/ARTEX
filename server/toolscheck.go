// toolscheck.go 启动期外部工具完整性自检（期 6 部署链加固）:server.New 时读
// 钉版清单（packaging/tools-manifest.json，或 ARTEX_TOOLS_MANIFEST 指定）,
// 对存在的二进制校 sha256（清单里非空的才校）。清单文件缺失（开发态）静默跳过；
// 工具缺失是常态（精简环境只装部分工具），只打一行汇总不刷屏；哈希不匹配只警告
// 不阻断（用户可能自编译）。详细逐项报告用 `artex doctor`。
package server

import (
	"log"
	"os"
	"strings"

	"github.com/Autumn-27/artex/tools"
)

// checkToolsManifest 执行启动自检并打日志；任何失败都只记日志，不拖垮启动。
func checkToolsManifest(dataDir string) {
	m, err := tools.Load(tools.ManifestPath())
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("[tools] 清单解析失败（跳过自检）: %v", err)
		}
		return
	}
	var ok, unpinned, missing, mismatch []string
	for _, r := range tools.Check(m, dataDir) {
		switch r.Status {
		case tools.StatusOK:
			ok = append(ok, r.Entry.Name)
		case tools.StatusUnpinned:
			unpinned = append(unpinned, r.Entry.Name)
		case tools.StatusMismatch:
			mismatch = append(mismatch, r.Entry.Name)
			log.Printf("[tools] ⚠ %s 哈希不匹配（期望 %s，实际 %s)——可能是自编译/新版，仅警告不阻断",
				r.Entry.Name, r.Entry.SHA256, r.Actual)
		case tools.StatusMissing:
			missing = append(missing, r.Entry.Name)
		}
	}
	log.Printf("[tools] 清单自检：sha256 一致 %d，存在未钉 %d，缺失 %d，不匹配 %d%s%s",
		len(ok), len(unpinned), len(missing), len(mismatch),
		toolsListNote("缺失", missing), toolsListNote("不匹配", mismatch))
}

// toolsListNote 把名单拼成汇总行后缀；空名单不输出（别刷屏）。
func toolsListNote(label string, names []string) string {
	if len(names) == 0 {
		return ""
	}
	return ";" + label + ": " + strings.Join(names, ", ")
}
