// Package app 的 pprof 诊断端点（fork 独有）。
//
// 为什么需要它：本项目出现过「会话发消息后长时间无任何响应、日志里一行错误都没有」
// 的故障（详见 FORK.md）。这类故障的共性是**某个 goroutine 停在某一行不动**——
// 而日志只能记录「发生了什么」，对「什么都没发生」无能为力。pprof 的 goroutine
// profile 恰好补上这块：它把每个 goroutine 的完整调用栈、阻塞原因与阻塞时长打出来，
// 一次采样即可定位「谁卡在哪、卡了多久、在等谁」，不必再逐个假设去试。
//
// 两个设计决定：
//
//  1. **独立端口而非挂在主路由上**。主路由是 chi，SPA 的 `handleFrontend` 是
//     兜底路由，任何未匹配的路径都会返回 index.html —— 直接访问
//     `<主端口>/debug/pprof/goroutine` 拿到的是前端 HTML。挂在主路由上需要在
//     chi 路由表里抢在兜底之前注册，会碰到上游文件；独立端口则完全绕开。
//  2. **只监听 loopback 且默认关闭**。goroutine 栈会带上函数参数值（会话 key、
//     文件路径等），不能暴露到局域网或公网。故地址固定为 127.0.0.1（不跟随 -addr
//     的外网绑定），且必须显式开启。
package app

import (
	"context"
	"log"
	"net"
	"net/http"
	_ "net/http/pprof" // 注册 /debug/pprof/* 到 http.DefaultServeMux
	"strings"
	"time"
)

// defaultPprofAddr 是诊断端口的默认监听地址。
//
// 固定 127.0.0.1（而不是跟随 -addr）是刻意的：-addr 常绑到 0.0.0.0 供局域网访问，
// 诊断端口若跟着走就会把 goroutine 栈暴露给整个局域网。
const defaultPprofAddr = "127.0.0.1:7767"

// pprofReadHeaderTimeout 防止慢速 header 攻击占用连接；与主服务的取值口径一致。
const pprofReadHeaderTimeout = 10 * time.Second

// StartPprofServer 在 addr（为空则用 defaultPprofAddr）上启动一个仅提供
// /debug/pprof/* 的诊断服务。
//
// 它**不返回错误**：诊断端点启动失败（端口被占等）绝不能影响主服务，只记日志。
// 调用点在 server.Start 里，紧挨着主 listener —— 这样 pprof 端口的启动失败不会
// 被误当成服务启动失败。
//
// 用法（服务所在机器上执行）：
//
//	curl -s http://127.0.0.1:7767/debug/pprof/goroutine?debug=2 > goroutines.txt
//	grep -B1 -A15 'minutes]' goroutines.txt     # 看阻塞最久的
//	go tool pprof http://127.0.0.1:7767/debug/pprof/goroutine   # 交互式
func StartPprofServer(ctx context.Context, addr string) {
	addr = normalizePprofAddr(addr)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		log.Printf("[pprof] listen.error addr=%s err=%v", addr, err)
		return
	}
	server := &http.Server{
		Handler:           http.DefaultServeMux,
		ReadHeaderTimeout: pprofReadHeaderTimeout,
	}
	go func() {
		<-ctx.Done()
		_ = server.Shutdown(context.Background())
	}()
	go func() {
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			log.Printf("[pprof] serve.error addr=%s err=%v", addr, err)
		}
	}()
	log.Printf("[pprof] listening addr=%s (loopback only, /debug/pprof/*)", listener.Addr().String())
}

// normalizePprofAddr 只接受 host:port 形式，并**强制 host 为 loopback**。
//
// 空值或解析不出 host:port 时回落到默认地址。非 loopback 的 host 一律改写成
// 127.0.0.1（而不是放行）——goroutine 栈会带出会话 key、文件路径等参数值，把这个
// 端点绑到局域网地址等于泄露内部状态。需要从别的机器抓栈时用 SSH 端口转发
// （`ssh -L 7767:127.0.0.1:7767 host`），不要放宽这里。
func normalizePprofAddr(addr string) string {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return defaultPprofAddr
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		log.Printf("[pprof] addr.invalid addr=%q err=%v; falling back to %s", addr, err, defaultPprofAddr)
		return defaultPprofAddr
	}
	if port == "" {
		log.Printf("[pprof] addr.invalid addr=%q: empty port; falling back to %s", addr, defaultPprofAddr)
		return defaultPprofAddr
	}
	if !isLoopbackHost(host) {
		log.Printf("[pprof] addr.forced_loopback from=%q host=%q", addr, host)
		return net.JoinHostPort("127.0.0.1", port)
	}
	return net.JoinHostPort(host, port)
}

// isLoopbackHost 报告 host 是否只指向本机。
//
// 认可三种写法：`localhost`、可解析且 IsLoopback() 的 IP（127.0.0.0/8、::1）、
// 以及空 host —— 空 host 在 net.Listen 语义里是「所有接口」，正是最危险的一种，
// 这里**不**认可，交给调用方改写成 127.0.0.1。
func isLoopbackHost(host string) bool {
	switch strings.TrimSpace(host) {
	case "localhost":
		return true
	case "":
		// net.Listen(":port") 会绑全部接口，不能放行。
		return false
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}
