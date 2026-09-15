package session

// bypass_test.go 验收测试：内嵌 freestanding .so 的 ELF 结构合法性、路径槽
// 改写、LD_PRELOAD 绕过探测全流程（httptest 模拟加固 PHP eval 马：exec 系全禁、
// mail/putenv 可用、模拟动态链接器执行构造器）。协议族级模拟，无特定靶场值。

import (
	"bytes"
	"context"
	"debug/elf"
	"encoding/binary"
	"strings"
	"testing"
)

// TestLDPreloadLibELF:内嵌 .so 必须是合法的 x86_64 ET_DYN，带 DT_INIT_ARRAY
// 指向 RX 段内构造器，且含路径槽 magic——这是「加载即证明」的全部结构前提。
func TestLDPreloadLibELF(t *testing.T) {
	blob := LDPreloadLib()
	if len(blob) == 0 {
		t.Fatal("go:embed 的 .so 为空（构建产物缺失）")
	}
	f, err := elf.NewFile(bytes.NewReader(blob))
	if err != nil {
		t.Fatalf("ELF 解析失败: %v", err)
	}
	if f.Class != elf.ELFCLASS64 || f.Machine != elf.EM_X86_64 || f.Type != elf.ET_DYN {
		t.Fatalf("class/machine/type = %v/%v/%v", f.Class, f.Machine, f.Type)
	}
	// PT_DYNAMIC 里必须有 DT_INIT_ARRAY（数组在 RW 段 0x1000，内容指向 0x100 构造器）。
	var initAddr, initSz uint64
	for _, p := range f.Progs {
		if p.Type != elf.PT_DYNAMIC {
			continue
		}
		d := blob[p.Off : p.Off+p.Filesz]
		for i := 0; i+16 <= len(d); i += 16 {
			tag := binary.LittleEndian.Uint64(d[i:])
			val := binary.LittleEndian.Uint64(d[i+8:])
			if tag == 25 {
				initAddr = val
			}
			if tag == 27 {
				initSz = val
			}
		}
	}
	if initAddr != 0x1000 || initSz != 8 {
		t.Fatalf("DT_INIT_ARRAY = %#x/%d, want 0x1000/8", initAddr, initSz)
	}
	if ctor := binary.LittleEndian.Uint64(blob[0x1000:]); ctor != 0x100 {
		t.Fatalf("init_array 内容 = %#x, want 0x100(构造器入口)", ctor)
	}
	if !bytes.Contains(blob, []byte(ldpreloadMagic)) {
		t.Fatal("缺路径槽 magic(patchLDPreloadPath 无法定位)")
	}
	// 构造器首字节必须是 mov eax,2(sys_open)——手搓机器码的最低自校验。
	if blob[0x100] != 0xB8 || blob[0x101] != 0x02 {
		t.Fatalf("构造器入口字节异常: % x", blob[0x100:0x108])
	}
}

// TestPatchLDPreloadPath:路径槽改写——写入路径、NUL 结尾、余量清零、异常输入。
func TestPatchLDPreloadPath(t *testing.T) {
	blob := LDPreloadLib()
	const mark = "/tmp/.artex_deadbeef.mark"
	out, err := patchLDPreloadPath(blob, mark)
	if err != nil {
		t.Fatalf("patch: %v", err)
	}
	i := bytes.Index(out, []byte(ldpreloadMagic))
	if i < 0 {
		t.Fatal("magic 缺失")
	}
	slot := out[i+len(ldpreloadMagic) : i+len(ldpreloadMagic)+ldpreloadPathLen]
	if !bytes.HasPrefix(slot, []byte(mark)) || slot[len(mark)] != 0 {
		t.Fatalf("槽内容异常: %q", slot[:len(mark)+8])
	}
	for _, b := range slot[len(mark):] {
		if b != 0 {
			t.Fatal("槽余量未清零（会残留上一次路径）")
		}
	}
	// 原 blob 不被修改。
	if !bytes.Equal(blob, LDPreloadLib()) {
		t.Fatal("patch 改了原始内嵌 blob")
	}
	if _, err := patchLDPreloadPath(blob, ""); err == nil {
		t.Fatal("空路径应报错")
	}
	if _, err := patchLDPreloadPath(blob, strings.Repeat("a", ldpreloadPathLen)); err == nil {
		t.Fatal("超长路径应报错")
	}
	if _, err := patchLDPreloadPath([]byte("no magic here"), "/tmp/x"); err == nil {
		t.Fatal("缺 magic 应报错")
	}
}

// 加固 PHP eval 马：exec 系全禁 + mail/putenv 可用（复盘 R1 的实战形态）。
func hardenedPHPMock(works bool) *phpMock {
	mock := newPHPMock("eval")
	for _, fn := range phpExecFuncs {
		mock.disable(fn)
	}
	mock.mu.Lock()
	mock.mailOn, mock.putenvOn, mock.bypassWorks = true, true, works
	mock.mu.Unlock()
	return mock
}

// TestProbeBypassLDPreloadConfirmed:构造器探活确认 → bypass=ld_preload 登记，
// .so 留档目标侧，Secret 落库往返后绕过信息不丢。
func TestProbeBypassLDPreloadConfirmed(t *testing.T) {
	mock := hardenedPHPMock(true)
	u := probeURL(t, mock)
	sh, info, err := Probe(context.Background(), u, "cmd", "auto")
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if !info.DisableFunctions || !info.Mail || !info.Putenv {
		t.Fatalf("指纹 = %+v", info)
	}
	if info.Bypass != "ld_preload" || info.BypassLib == "" {
		t.Fatalf("bypass 未登记: %+v", info)
	}
	if !strings.Contains(info.BypassNote, "探活确认") {
		t.Fatalf("BypassNote = %q", info.BypassNote)
	}
	// .so 已投递且留档（供后续 session_exec 绕过执行留口）。
	if so, ok := mock.fs.readFile(info.BypassLib); !ok || len(so) != len(LDPreloadLib()) {
		t.Fatalf(".so 投递异常: ok=%v len=%d", ok, len(so))
	}
	// 投递的 .so 路径槽已被改写为本次探活标记路径（非出厂全零）。
	so, _ := mock.fs.readFile(info.BypassLib)
	i := bytes.Index(so, []byte(ldpreloadMagic))
	slot := so[i+len(ldpreloadMagic) : i+len(ldpreloadMagic)+ldpreloadPathLen]
	if !strings.HasPrefix(string(slot), defaultTmpPrefix) || !strings.HasSuffix(strings.TrimRight(string(slot), "\x00"), ".mark") {
		t.Fatalf("路径槽未改写为探活标记路径: %q", slot)
	}
	// disable_functions 下的 Exec 错误必须点名绕过登记与留口，不轻言「被查杀」。
	_, _, err = sh.Exec(context.Background(), "id", 0)
	if err == nil || !strings.Contains(err.Error(), "ld_preload") || !strings.Contains(err.Error(), "留口") {
		t.Fatalf("Exec 错误语义 = %v", err)
	}
	// Secret 落库往返：绕过信息随恢复带回，免重探。
	sec := sh.Secret()
	if !sec.DisableFunctions || sec.Bypass != "ld_preload" || sec.BypassLib == "" {
		t.Fatalf("secret = %+v", sec)
	}
	got, err := RestoreHTTPShell(9, sec)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	_, _, err = got.Exec(context.Background(), "id", 0)
	if err == nil || !strings.Contains(err.Error(), "ld_preload") {
		t.Fatalf("恢复后 Exec 错误语义 = %v", err)
	}
}

// TestProbeBypassConstructorNotRun:构造器未执行（mail 未派生子进程/加载器拒载）
// → 如实报不可用并清理已投递 .so，不伪造可用。
func TestProbeBypassConstructorNotRun(t *testing.T) {
	mock := hardenedPHPMock(false)
	u := probeURL(t, mock)
	_, info, err := Probe(context.Background(), u, "cmd", "auto")
	if err != nil {
		t.Fatalf("登记不应被绕过探测拖垮: %v", err)
	}
	if info.Bypass != "" || info.BypassLib != "" {
		t.Fatalf("不应伪造绕过可用: %+v", info)
	}
	if !strings.Contains(info.BypassNote, "构造器未执行") {
		t.Fatalf("BypassNote = %q", info.BypassNote)
	}
	if mock.fs.hasPrefixMatch(defaultTmpPrefix) {
		t.Fatal("绕过不可用时已投递的 .so 应被清理")
	}
}

// TestProbeBypassPreconditionMissing:mail/putenv 不全可用 → 直接记前提不满足，
// 不发投递/触发探针。
func TestProbeBypassPreconditionMissing(t *testing.T) {
	mock := newPHPMock("eval")
	for _, fn := range phpExecFuncs {
		mock.disable(fn)
	}
	u := probeURL(t, mock)
	_, info, err := Probe(context.Background(), u, "cmd", "php")
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if !info.DisableFunctions || info.Mail || info.Putenv {
		t.Fatalf("指纹 = %+v", info)
	}
	if info.Bypass != "" || !strings.Contains(info.BypassNote, "前提不满足") {
		t.Fatalf("bypass 结论 = %+v", info)
	}
	if mock.fs.hasPrefixMatch(defaultTmpPrefix) {
		t.Fatal("前提不满足时不应投递任何文件")
	}
}
