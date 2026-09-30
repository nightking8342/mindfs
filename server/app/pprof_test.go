package app

import (
	"net"
	"testing"
)

// TestNormalizePprofAddr 固化 pprof 诊断端点的地址安全契约。
//
// 这个端点的定位是「本机自查」：goroutine 栈会带上函数参数值（会话 key、文件路径
// 等），绝不能暴露到局域网。因此有一条硬规则 —— **host 只允许 loopback**，写
// 0.0.0.0 / :: / 空 host 一律强制回落 127.0.0.1，而不是照单全收。
//
// 需要从别的机器抓栈时走 SSH 端口转发，不要靠改这个函数放宽。
func TestNormalizePprofAddr(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want string
	}{
		{"empty falls back to default", "", defaultPprofAddr},
		{"blank falls back to default", "   ", defaultPprofAddr},
		{"loopback kept", "127.0.0.1:7767", "127.0.0.1:7767"},
		{"loopback other port kept", "127.0.0.1:9999", "127.0.0.1:9999"},
		{"localhost kept", "localhost:7767", "localhost:7767"},

		// 以下三条是安全契约的核心：不得暴露到非 loopback。
		{"0.0.0.0 forced to loopback", "0.0.0.0:7767", "127.0.0.1:7767"},
		{"empty host forced to loopback", ":7767", "127.0.0.1:7767"},
		{"ipv6 any forced to loopback", "[::]:7767", "127.0.0.1:7767"},

		// 缺失端口 / 非 host:port 形式一律回落默认值，不猜测、不放行。
		{"bare host falls back", "127.0.0.1", defaultPprofAddr},
		{"missing port falls back", "127.0.0.1:", defaultPprofAddr},
		{"garbage falls back", "not-an-address", defaultPprofAddr},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizePprofAddr(tc.in); got != tc.want {
				t.Fatalf("normalizePprofAddr(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestNormalizePprofAddrNeverExposesNonLoopback 单独把「不得出现非 loopback host」
// 抽成一条断言，避免日后有人新增用例时无意放宽上面那张表。
func TestNormalizePprofAddrNeverExposesNonLoopback(t *testing.T) {
	for _, in := range []string{
		"0.0.0.0:7767", ":7767", "[::]:7767",
		"192.168.1.31:7767", "example.com:7767",
	} {
		got := normalizePprofAddr(in)
		host, port, err := net.SplitHostPort(got)
		if err != nil {
			t.Fatalf("normalizePprofAddr(%q) = %q: 不是合法的 host:port", in, got)
		}
		if port == "" {
			t.Fatalf("normalizePprofAddr(%q) = %q: 端口为空", in, got)
		}
		if !isLoopbackHost(host) {
			t.Fatalf("normalizePprofAddr(%q) = %q：host %q 不是 loopback，诊断端点会被暴露", in, got, host)
		}
	}
}
