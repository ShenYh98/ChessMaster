// Package webui - 浏览器自启动辅助
package webui

import (
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"syscall"
)

// LaunchBrowser 优先以"独立应用窗口"模式（Edge/Chrome 的 --app）打开 URL，
// 这样窗口不带浏览器导航栏，并且我们能拿到进程句柄以便程序退出时关闭它。
// 如果未找到合适的浏览器，会回退默认浏览器并返回 nil（无法主动关闭）。
func LaunchBrowser(url string) *exec.Cmd {
	if runtime.GOOS != "windows" {
		// 非 Windows：简单调用 xdg-open / open
		opener := "xdg-open"
		if runtime.GOOS == "darwin" {
			opener = "open"
		}
		_ = exec.Command(opener, url).Start()
		return nil
	}

	// 1. 优先尝试 Edge / Chrome 的 --app 模式，可独立成 PWA 风格窗口
	candidates := []string{
		`C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe`,
		`C:\Program Files\Microsoft\Edge\Application\msedge.exe`,
		`C:\Program Files\Google\Chrome\Application\chrome.exe`,
		`C:\Program Files (x86)\Google\Chrome\Application\chrome.exe`,
	}
	// 用独立的 user-data-dir，避免占用用户已开浏览器的会话
	tmpDir, _ := os.MkdirTemp("", "chessmaster-webui-")
	for _, exe := range candidates {
		if _, err := os.Stat(exe); err != nil {
			continue
		}
		args := []string{
			"--app=" + url,
			"--new-window",
		}
		if tmpDir != "" {
			args = append(args, "--user-data-dir="+tmpDir)
		}
		cmd := exec.Command(exe, args...)
		// 隐藏命令窗口（仅 Windows）
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
		if err := cmd.Start(); err == nil {
			return cmd
		}
	}

	// 2. 回退到默认浏览器（拿不到进程句柄）
	_ = exec.Command("cmd", "/c", "start", "", url).Start()
	return nil
}

// CloseBrowser 优雅关闭由 LaunchBrowser 启动的浏览器进程。
// Chrome/Edge 启动后会 fork 出多个子进程（GPU、渲染、子窗口等），
// 必须以进程树方式一起 Kill，否则独立窗口依然存活。
func CloseBrowser(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	pid := cmd.Process.Pid
	if runtime.GOOS == "windows" {
		// /T = 终止进程树   /F = 强制
		kill := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(pid))
		kill.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
		_ = kill.Run()
	} else {
		_ = cmd.Process.Kill()
	}
	_, _ = cmd.Process.Wait()
}
