package agent

import (
	"fmt"
	"hash/fnv"
	"regexp"
	"strings"
	"unicode/utf8"
)

// chains L1(CHAINS-INTEGRATION-DESIGN.md §3 L1 行):worker 循环内的同质响应停滞检测。
// 复盘证据 POSTMORTEM-RED-SUN-3.md R4:同一崩溃签名(malloc(): memory corruption)连出
// 十几轮仍重试,169 拍烧光;chains meta 的"15~25 次同质响应即转向"规则只在提示词里、
// 没进代码。本文件把它落成确定性判据:纯函数、无 I/O,由调用方(engine 的 steerHooks,
// PostToolUse 观察点)把每步 tool_result 喂进来;触发后经 steer 注入路径(PreToolUse
// drain)打断下一次工具调用,把转向指令还给模型。

// StuckConfig 是检测参数(默认即线上值;单测可缩小阈值缩短用例)。
type StuckConfig struct {
	Window         int     // 滚动窗口:保留最近 N 个 tool_result 参与判定
	SigPrefix      int     // 归一化输出取前 N 字符进签名哈希
	SameSigTrigger int     // 同一签名连续 >= N 次 → 触发
	FailTrigger    int     // 同工具连续失败 >= N 次且输出相似 → 触发
	FailSimilarity float64 // 上条的相似度阈值(0~1,bigram Dice 均值)
	Cooldown       int     // 触发后 N 步(tool_result)内不再触发
	MaxFires       int     // 每个检测器(=每条意图)最多触发次数
}

// DefaultStuckConfig 返回线上默认:同签名连续 12 次、或同工具连续失败 15 次且相似度
// >0.8 判定"信息梯度为零";触发后冷却 10 步;每意图最多 3 次。
func DefaultStuckConfig() StuckConfig {
	return StuckConfig{
		Window: 25, SigPrefix: 200,
		SameSigTrigger: 12, FailTrigger: 15, FailSimilarity: 0.8,
		Cooldown: 10, MaxFires: 3,
	}
}

// 归一化规则(顺序敏感):时间戳 → 路径/URL → 长 hex → 长随机串 → 数字 → 空白折叠。
// 目的:同一拦截页/同一崩溃输出里嵌着的时间戳、请求 id、临时路径、随机 token 不该
// 把"同质"伪装成"不同"。
var (
	stuckReTimestamp = regexp.MustCompile(`\d{4}[-/]\d{1,2}[-/]\d{1,2}([t ]\d{1,2}:\d{2}(:\d{2})?(\.\d+)?(z|[+-]\d{2}:?\d{2})?)?|\d{1,2}:\d{2}:\d{2}(\.\d+)?`)
	stuckRePath      = regexp.MustCompile(`(?:[a-z]:)?[\w.~+-]*(?:[/\\][\w.~+-]+)+/?`)
	stuckReHex       = regexp.MustCompile(`\b[0-9a-f]{8,}\b`)
	stuckReRand      = regexp.MustCompile(`\b[\w+-]{24,}\b`)
	stuckReDigits    = regexp.MustCompile(`\d+`)
	stuckReSpace     = regexp.MustCompile(`\s+`)
)

// normalizeStuckOutput 把工具输出归一化成可比较的形态:小写化后剥离时间戳、路径、
// 长 hex/随机串、数字,再折叠空白。
func normalizeStuckOutput(s string) string {
	s = strings.ToLower(s)
	s = stuckReTimestamp.ReplaceAllString(s, "<ts>")
	s = stuckRePath.ReplaceAllString(s, "<path>")
	s = stuckReHex.ReplaceAllString(s, "<hex>")
	s = stuckReRand.ReplaceAllString(s, "<rand>")
	s = stuckReDigits.ReplaceAllString(s, "#")
	s = stuckReSpace.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

// stuckSignature 是"响应签名":工具名 + 归一化输出的 FNV-1a 哈希。同签名必同工具。
func stuckSignature(tool, norm string) string {
	h := fnv.New64a()
	_, _ = h.Write([]byte(norm))
	return tool + ":" + fmt.Sprintf("%016x", h.Sum64())
}

// bigramDice 是两个归一化字符串的 rune-bigram Dice 相似度(0~1)。输出为空串对时
// 视为 1(两边都什么都没有 = 同质)。
func bigramDice(a, b string) float64 {
	if a == b {
		return 1
	}
	ra, rb := []rune(a), []rune(b)
	if len(ra) < 2 || len(rb) < 2 {
		return 0
	}
	grams := make(map[[2]rune]int, len(ra)-1)
	for i := 0; i+1 < len(ra); i++ {
		grams[[2]rune{ra[i], ra[i+1]}]++
	}
	overlap := 0
	for i := 0; i+1 < len(rb); i++ {
		g := [2]rune{rb[i], rb[i+1]}
		if grams[g] > 0 {
			grams[g]--
			overlap++
		}
	}
	return 2 * float64(overlap) / float64(len(ra)+len(rb)-2)
}

// StuckEvent 是一次停滞判定的结果。
type StuckEvent struct {
	Tool      string // 卡住的工具名
	Kind      string // "same_signature" | "same_tool_failure"
	Count     int    // 触发时的连续同质/连续失败次数
	Signature string // 命中的响应签名短码(哈希前 8 位)
	N         int    // 本意图内第几次触发(1 起)
}

type stuckEntry struct {
	tool  string
	sig   string
	norm  string
	isErr bool
}

// StuckDetector 是滚动停滞检测器:每条意图一个(建在 model_error 重跑循环外,重跑
// 不清零额度),由调用方逐个喂 tool_result。非并发安全——harness 的 hook 串行调用,
// 与 steerHooks 的既有假设一致。
type StuckDetector struct {
	cfg        StuckConfig
	recent     []stuckEntry // 滚动窗口, cap = cfg.Window
	consecSig  int          // 最新签名的连续出现次数
	consecFail int          // 最新工具的连续失败次数
	cooldown   int          // 剩余冷却步数(>0 期间不触发)
	fires      int          // 已触发次数
	pending    []string     // 待注入的干预消息(PreToolUse drain,FIFO)
	// PivotHint 是触发时追加在干预消息后的类别化转向建议(chainskel.PivotHints
	// 的产物,按意图 chain_tags 在检测器创建时一次性算好,整条意图不变)。
	// 空 = 不追加(无匹配类别时维持通用措辞)。
	PivotHint string
}

func NewStuckDetector(cfg StuckConfig) *StuckDetector {
	return &StuckDetector{cfg: cfg}
}

// Observe 喂入一步 tool_result;判定"信息梯度为零"时返回事件并把干预消息排入
// pending(供 PreToolUse 在下一次工具调用前 drain),否则返回 nil。冷却期与触发
// 上限内的同质输入照常统计(冷却一结束、仍同质可再触发,直到 MaxFires 用尽)。
func (d *StuckDetector) Observe(tool, output string, isErr bool) *StuckEvent {
	norm := normalizeStuckOutput(output)
	if d.cfg.SigPrefix > 0 && utf8.RuneCountInString(norm) > d.cfg.SigPrefix {
		norm = string([]rune(norm)[:d.cfg.SigPrefix])
	}
	e := stuckEntry{tool: tool, sig: stuckSignature(tool, norm), norm: norm, isErr: isErr}

	if n := len(d.recent); n > 0 && d.recent[n-1].sig == e.sig {
		d.consecSig++
	} else {
		d.consecSig = 1
	}
	if n := len(d.recent); isErr && n > 0 && d.recent[n-1].isErr && d.recent[n-1].tool == tool {
		d.consecFail++
	} else if isErr {
		d.consecFail = 1
	} else {
		d.consecFail = 0
	}

	d.recent = append(d.recent, e)
	if d.cfg.Window > 0 && len(d.recent) > d.cfg.Window {
		d.recent = append([]stuckEntry(nil), d.recent[1:]...) // 拷贝滚窗,避免底层数组无限右漂
	}

	if d.cooldown > 0 {
		d.cooldown--
		return nil
	}
	if d.fires >= d.cfg.MaxFires {
		return nil
	}

	var ev *StuckEvent
	switch {
	case d.consecSig >= d.cfg.SameSigTrigger:
		ev = &StuckEvent{Tool: tool, Kind: "same_signature", Count: d.consecSig, Signature: shortStuckSig(e.sig)}
	case d.consecFail >= d.cfg.FailTrigger && d.failSimilar():
		ev = &StuckEvent{Tool: tool, Kind: "same_tool_failure", Count: d.consecFail, Signature: shortStuckSig(e.sig)}
	}
	if ev == nil {
		return nil
	}
	d.fires++
	ev.N = d.fires
	d.cooldown = d.cfg.Cooldown
	msg := StuckIntervention(*ev)
	if d.PivotHint != "" {
		msg += "\n" + d.PivotHint
	}
	d.pending = append(d.pending, msg)
	return ev
}

// Drain 弹出最早一条待注入的干预消息(PreToolUse 在下一次工具调用前调用)。
func (d *StuckDetector) Drain() (string, bool) {
	if len(d.pending) == 0 {
		return "", false
	}
	msg := d.pending[0]
	d.pending = d.pending[1:]
	return msg, true
}

// Fires 返回已触发次数(观测/日志用)。
func (d *StuckDetector) Fires() int { return d.fires }

// failSimilar 判定最近连续失败是否"同质":取最近 FailTrigger 个连续失败,逐个与
// 最新一条算 bigram Dice,均值须 > FailSimilarity。
func (d *StuckDetector) failSimilar() bool {
	n := d.cfg.FailTrigger
	if n <= 1 || len(d.recent) < n {
		return false
	}
	tail := d.recent[len(d.recent)-n:]
	latest := tail[n-1].norm
	sum := 0.0
	for _, e := range tail[:n-1] {
		sum += bigramDice(e.norm, latest)
	}
	return sum/float64(n-1) > d.cfg.FailSimilarity
}

// shortStuckSig 取签名的哈希段前 8 位,给提示/日志展示用。
func shortStuckSig(sig string) string {
	if i := strings.IndexByte(sig, ':'); i >= 0 {
		sig = sig[i+1:]
	}
	if len(sig) > 8 {
		sig = sig[:8]
	}
	return sig
}

// StuckIntervention 是触发时注入 worker 会话的系统提示文本。措辞与 chains meta
// "同质响应即转向"规则一致:要么换维度,要么写回 fact 标记榨干并请求转向。
func StuckIntervention(ev StuckEvent) string {
	what := "完全同质"
	if ev.Kind == "same_tool_failure" {
		what = "高度相似的连续失败"
	}
	tool := ev.Tool
	if tool == "" {
		tool = "?"
	}
	return fmt.Sprintf("【停滞检测】最近 %d 次响应%s(签名 %s,工具 %s),信息梯度为零。"+
		"立即停止同轴重试:要么换维度(参数/信道/漏洞类/入口),要么写回 fact 标记此方向榨干并请求转向。"+
		"(本次工具调用未执行,先响应这条系统指令;这是本意图第 %d 次停滞提醒,提醒次数有限。)",
		ev.Count, what, ev.Signature, tool, ev.N)
}
