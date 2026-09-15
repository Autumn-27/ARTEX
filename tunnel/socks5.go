// socks5.go 是极简 SOCKS5 客户端（无认证），只够做隧道内决定性验证：
// 经平台回环上的反向 socks 入口真实 CONNECT 一个目标侧可达地址。报文组装与
// 应答解析是纯函数，可单测。
package tunnel

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strconv"
	"time"
)

// BuildSocks5Greeting 组装握手报文 [VER=5, NMETHODS=1, NOAUTH]。纯函数。
func BuildSocks5Greeting() []byte {
	return []byte{0x05, 0x01, 0x00}
}

// BuildSocks5Request 组装 CONNECT 请求（域名或 IPv4)。纯函数。
func BuildSocks5Request(host string, port int) ([]byte, error) {
	if port <= 0 || port > 65535 {
		return nil, fmt.Errorf("非法端口 %d", port)
	}
	out := []byte{0x05, 0x01, 0x00}
	if ip := net.ParseIP(host); ip != nil {
		if v4 := ip.To4(); v4 != nil {
			out = append(out, 0x01)
			out = append(out, v4...)
		} else {
			out = append(out, 0x04)
			out = append(out, ip.To16()...)
		}
	} else {
		if len(host) > 255 {
			return nil, fmt.Errorf("域名过长 %d", len(host))
		}
		out = append(out, 0x03, byte(len(host)))
		out = append(out, host...)
	}
	var p [2]byte
	binary.BigEndian.PutUint16(p[:], uint16(port))
	return append(out, p[:]...), nil
}

// ParseSocks5Reply 解析 CONNECT 应答：返回是否成功与状态码。纯函数。
func ParseSocks5Reply(b []byte) (bool, byte) {
	if len(b) < 2 || b[0] != 0x05 {
		return false, 0xff
	}
	return b[1] == 0x00, b[1]
}

// BuildSocks5GreetingAuth 组装声明 user/pass 方法的握手报文 [VER=5, NMETHODS=1, USERPASS=2]。
// 纯函数。
func BuildSocks5GreetingAuth() []byte {
	return []byte{0x05, 0x01, 0x02}
}

// BuildSocks5UserPass 组装 RFC 1929 用户名/口令子协商报文。纯函数。
func BuildSocks5UserPass(user, pass string) ([]byte, error) {
	if len(user) == 0 || len(user) > 255 || len(pass) > 255 {
		return nil, fmt.Errorf("socks5 auth 用户名/口令长度非法（1-255)")
	}
	out := []byte{0x01, byte(len(user))}
	out = append(out, user...)
	out = append(out, byte(len(pass)))
	return append(out, pass...), nil
}

// ParseSocks5UserPassReply 解析子协商应答（VER=1, STATUS=0 为成功）。纯函数。
func ParseSocks5UserPassReply(b []byte) bool {
	return len(b) >= 2 && b[0] == 0x01 && b[1] == 0x00
}

// socks5DialAuth 经带 user/pass 鉴权的 socks5 入口 addr 拨测 target(suo5 --auth 场景)。
func socks5DialAuth(socksAddr, target, user, pass string, timeout time.Duration) error {
	host, portStr, err := net.SplitHostPort(target)
	if err != nil {
		return fmt.Errorf("验证目标 %q 解析失败： %w", target, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return fmt.Errorf("验证目标 %q 端口非法： %w", target, err)
	}
	conn, err := net.DialTimeout("tcp", socksAddr, timeout)
	if err != nil {
		return fmt.Errorf("socks 入口 %s 不可连： %w", socksAddr, err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))
	if _, err := conn.Write(BuildSocks5GreetingAuth()); err != nil {
		return err
	}
	buf := make([]byte, 2)
	if _, err := io.ReadFull(conn, buf); err != nil {
		return fmt.Errorf("socks 握手读失败： %w", err)
	}
	if buf[0] != 0x05 || buf[1] != 0x02 {
		return fmt.Errorf("socks 服务端不接受 user/pass 鉴权（method=%d)", buf[1])
	}
	auth, err := BuildSocks5UserPass(user, pass)
	if err != nil {
		return err
	}
	if _, err := conn.Write(auth); err != nil {
		return err
	}
	if _, err := io.ReadFull(conn, buf); err != nil {
		return fmt.Errorf("socks auth 应答读失败： %w", err)
	}
	if !ParseSocks5UserPassReply(buf) {
		return fmt.Errorf("socks auth 被拒（口令错？)")
	}
	req, err := BuildSocks5Request(host, port)
	if err != nil {
		return err
	}
	if _, err := conn.Write(req); err != nil {
		return err
	}
	reply := make([]byte, 4)
	if _, err := io.ReadFull(conn, reply); err != nil {
		return fmt.Errorf("socks 应答读失败： %w", err)
	}
	ok, code := ParseSocks5Reply(reply)
	if !ok {
		return fmt.Errorf("socks CONNECT %s 被拒（rep=0x%02x)", target, code)
	}
	return nil
}

// socks5Dial 经 socks5 入口 addr 拨测 target(host:port)。连通即决定性证据。
func socks5Dial(socksAddr, target string, timeout time.Duration) error {
	host, portStr, err := net.SplitHostPort(target)
	if err != nil {
		return fmt.Errorf("验证目标 %q 解析失败: %w", target, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return fmt.Errorf("验证目标 %q 端口非法: %w", target, err)
	}
	conn, err := net.DialTimeout("tcp", socksAddr, timeout)
	if err != nil {
		return fmt.Errorf("socks 入口 %s 不可连: %w", socksAddr, err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))
	if _, err := conn.Write(BuildSocks5Greeting()); err != nil {
		return err
	}
	buf := make([]byte, 2)
	if _, err := io.ReadFull(conn, buf); err != nil {
		return fmt.Errorf("socks 握手读失败: %w", err)
	}
	if buf[0] != 0x05 || buf[1] != 0x00 {
		return fmt.Errorf("socks 握手被拒（method=%d)", buf[1])
	}
	req, err := BuildSocks5Request(host, port)
	if err != nil {
		return err
	}
	if _, err := conn.Write(req); err != nil {
		return err
	}
	reply := make([]byte, 4)
	if _, err := io.ReadFull(conn, reply); err != nil {
		return fmt.Errorf("socks 应答读失败: %w", err)
	}
	ok, code := ParseSocks5Reply(reply)
	if !ok {
		return fmt.Errorf("socks CONNECT %s 被拒（rep=0x%02x)", target, code)
	}
	return nil
}
