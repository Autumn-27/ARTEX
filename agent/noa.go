package agent

import (
	"log"
	"path/filepath"

	"github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/noaadapter"
)

// noaWarn returns a diagnostics sink tagging non-fatal noa messages with the
// session, routed through the package logger (agents have no per-instance one).
func noaWarn(session string) func(string) {
	return func(msg string) { log.Printf("[noa] %s: %s", session, msg) }
}

// noa 是 norma v0.4.0 引入的「模型驱动上下文压缩」机制,作为平台实验功能由用户在
// 系统设置中开关。它与内置 compaction 互斥:noaadapter.Enable 是唯一入口,一次挂上
// 上下文接管器(Compactor)、Compress 工具与三段常驻提示词,不调用 Enable 即为关闭
// (内置 compaction 照常工作)。开关由每个 agent 注入的 noaEnabledFn 解析,每 run 读
// 一次,故切换只影响之后启动的 run,无需重建 agent。

// enableNoa 在解析器报告开启时把 noa 接入 opts。archiveRoot 是压缩原文的持久化基目录
// (取全局 workDir,各 agent 统一落在 <workDir>/noa 下,不随任务/意图目录分散),sessionID
// 命名其下的归档子目录(全局唯一,故同一基目录内不冲突)。
//
// noa 是实验功能:接入失败不得中断真实任务。发生错误时经 onWarn 上报并回退内置压缩。
// 启用成功时清掉 opts.Compaction,避免 agentcore 因「两个上下文管理器同时设置」告警。
func enableNoa(opts *agentcore.Options, enabled func() bool, archiveRoot, sessionID string, onWarn func(string)) {
	if enabled == nil || !enabled() {
		return
	}
	if opts.OnWarn == nil {
		opts.OnWarn = onWarn
	}
	if err := noaadapter.Enable(opts, noaadapter.Options{
		ArchiveBaseDir: filepath.Join(archiveRoot, "noa"),
		SessionID:      sessionID,
		OnWarn:         onWarn,
	}); err != nil {
		if onWarn != nil {
			onWarn("noa 压缩启用失败,回退内置压缩:" + err.Error())
		}
		return
	}
	// Compactor 覆盖 Compaction,但两者并存时 agentcore 每次会告警;明确清掉。
	opts.Compaction = nil
}
