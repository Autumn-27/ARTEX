package server

import (
	"testing"
)

// /proc/net/tcp fixture:header + 已知端口(8787 loopback)+ 台账外 0.0.0.0:9999
// + 非 loopback 10.0.0.5:8080 + 非 LISTEN 行。
const procNetTCPFixture = `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 0100007F:2217 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 12345 1 0000000000000000 100 0 0 10 0
   1: 00000000:270F 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 12346 1 0000000000000000 100 0 0 10 0
   2: 0500000A:1F90 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 12347 1 0000000000000000 100 0 0 10 0
   3: 0100007F:0016 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 12348 1 0000000000000000 100 0 0 10 0
   4: 0100007F:2217 0100007F:9C40 01 00000000:00000000 00:00000000 00000000     0        0 12349 1 0000000000000000 100 0 0 10 0
`

// /proc/net/tcp6 fixture:::1:8787(loopback,跳过)+ :::8081(全接口,台账外)。
const procNetTCP6Fixture = `  sl  local_address                         remote_address                        st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 00000000000000000000000001000000:2217 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 22345 1 0000000000000000 100 0 0 10 0
   1: 00000000000000000000000000000000:1F91 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 22346 1 0000000000000000 100 0 0 10 0
`

func TestParseProcNetTCP(t *testing.T) {
	socks := parseProcNetTCP(procNetTCPFixture)
	if len(socks) != 4 { // 只有 0A(LISTEN)行,established(01)被跳过
		t.Fatalf("listeners = %d, want 4: %+v", len(socks), socks)
	}
	// 0100007F:2217 → 127.0.0.1:8727? 0x2217 = 8727... 用断言锚定真实换算。
	if got := socks[0]; got.IP.String() != "127.0.0.1" || got.Port != 0x2217 {
		t.Fatalf("sock[0] = %s:%d", got.IP, got.Port)
	}
	if got := socks[1]; !got.IP.IsUnspecified() || got.Port != 9999 {
		t.Fatalf("sock[1] = %s:%d, want 0.0.0.0:9999", got.IP, got.Port)
	}
	if got := socks[2]; got.IP.String() != "10.0.0.5" || got.Port != 8080 {
		t.Fatalf("sock[2] = %s:%d, want 10.0.0.5:8080", got.IP, got.Port)
	}
}

func TestParseProcNetTCP6(t *testing.T) {
	socks := parseProcNetTCP(procNetTCP6Fixture)
	if len(socks) != 2 {
		t.Fatalf("listeners = %d, want 2: %+v", len(socks), socks)
	}
	if got := socks[0]; !got.IP.IsLoopback() {
		t.Fatalf("sock[0] = %s, want ::1", got.IP)
	}
	if got := socks[1]; !got.IP.IsUnspecified() || got.Port != 8081 {
		t.Fatalf("sock[1] = %s:%d, want :::8081", got.IP, got.Port)
	}
}

func TestKnownManagedPortsCoversPlatform(t *testing.T) {
	known := knownManagedPorts()
	for _, p := range []int{8787, 8788, 22} {
		if !known[p] {
			t.Fatalf("port %d should be on the platform ledger", p)
		}
	}
	if known[9999] {
		t.Fatal("9999 should NOT be on the ledger")
	}
}
