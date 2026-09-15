// socks5_test.go 是 SOCKS5 报文组装/应答解析的纯函数单测。
package tunnel

import (
	"bytes"
	"testing"
)

func TestBuildSocks5Greeting(t *testing.T) {
	if g := BuildSocks5Greeting(); !bytes.Equal(g, []byte{0x05, 0x01, 0x00}) {
		t.Errorf("greeting = %v", g)
	}
}

func TestBuildSocks5Request(t *testing.T) {
	// IPv4
	req, err := BuildSocks5Request("127.0.0.1", 8080)
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{0x05, 0x01, 0x00, 0x01, 127, 0, 0, 1, 0x1f, 0x90}
	if !bytes.Equal(req, want) {
		t.Errorf("IPv4 request = %v, want %v", req, want)
	}
	// 域名
	req, err = BuildSocks5Request("web.local", 443)
	if err != nil {
		t.Fatal(err)
	}
	want = append([]byte{0x05, 0x01, 0x00, 0x03, 9}, []byte("web.local")...)
	want = append(want, 0x01, 0xbb)
	if !bytes.Equal(req, want) {
		t.Errorf("domain request = %v, want %v", req, want)
	}
	// 非法端口
	if _, err := BuildSocks5Request("127.0.0.1", 0); err == nil {
		t.Error("端口 0 应报错")
	}
	if _, err := BuildSocks5Request("127.0.0.1", 70000); err == nil {
		t.Error("端口越界应报错")
	}
}

func TestParseSocks5Reply(t *testing.T) {
	cases := []struct {
		name     string
		in       []byte
		wantOK   bool
		wantCode byte
	}{
		{"成功", []byte{0x05, 0x00, 0x00, 0x01}, true, 0x00},
		{"连接被拒", []byte{0x05, 0x05, 0x00, 0x01}, false, 0x05},
		{"网络不可达", []byte{0x05, 0x03, 0x00, 0x01}, false, 0x03},
		{"版本不对", []byte{0x04, 0x00}, false, 0xff},
		{"过短", []byte{0x05}, false, 0xff},
		{"空", nil, false, 0xff},
	}
	for _, c := range cases {
		ok, code := ParseSocks5Reply(c.in)
		if ok != c.wantOK || code != c.wantCode {
			t.Errorf("%s: ParseSocks5Reply = (%v,0x%02x), want (%v,0x%02x)", c.name, ok, code, c.wantOK, c.wantCode)
		}
	}
}
