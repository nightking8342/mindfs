package app

import (
	"os"
	"path/filepath"
	"testing"
)

// TestDefaultStartupConfigPath 固化 fork 的默认启动配置路径契约。
//
// 背景：上游的启动配置没有默认位置 —— -config 省略时 CLI 直接用硬编码的
// addr（127.0.0.1:7331），导致任务 agent 用 -from-task 汇报时探测不到服务、
// 进而自行启动一个空实例并向它汇报（实测于 vFlow 项目）。fork 让 -config
// 为空时回落到本函数返回的路径。
//
// 本测试锁住两件事：路径落在 MindFSConfigDir 下、且「文件不存在时 ok 为 false」
// （绝不能因为返回了路径就去读一个不存在的文件）。
func TestDefaultStartupConfigPath(t *testing.T) {
	home := t.TempDir()
	for _, key := range []string{"HOME", "USERPROFILE", "AppData", "XDG_CONFIG_HOME"} {
		t.Setenv(key, home)
	}
	// 让 os.UserConfigDir() 在本测试里指向临时目录（Windows 读 %AppData%）。
	t.Setenv("AppData", filepath.Join(home, "AppData", "Roaming"))

	configDir, err := os.UserConfigDir()
	if err != nil {
		t.Fatalf("UserConfigDir: %v", err)
	}
	want := filepath.Join(configDir, "mindfs", "config.json")

	// 1. 文件不存在 → 返回路径但 ok=false
	got, ok := DefaultStartupConfigPath()
	if ok {
		t.Errorf("配置文件不存在时 ok 应为 false，实际 true（path=%q）", got)
	}
	if got != want {
		t.Errorf("路径 = %q，期望 %q", got, want)
	}

	// 2. 文件存在 → ok=true，路径不变
	if err := os.MkdirAll(filepath.Dir(want), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(want, []byte(`{"addr":"127.0.0.1:7766","tls":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got2, ok2 := DefaultStartupConfigPath()
	if !ok2 {
		t.Fatalf("配置文件存在时 ok 应为 true（path=%q）", got2)
	}
	if got2 != want {
		t.Errorf("路径 = %q，期望 %q", got2, want)
	}
}
