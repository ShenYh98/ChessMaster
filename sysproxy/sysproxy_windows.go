//go:build windows

// Package sysproxy 管理 Windows 系统代理（HKCU 注册表 + WinInet 通知）
package sysproxy

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

const regPath = `HKCU\Software\Microsoft\Windows\CurrentVersion\Internet Settings`

// State 保存系统代理的原始状态，用于退出时还原
type State struct {
	Enabled   bool
	Server    string
	HasServer bool // 原本是否设置过 ProxyServer
}

// hideCmd 创建不弹黑窗的命令
func hideCmd(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return cmd
}

// Get 读取当前 Windows 系统代理状态
func Get() (*State, error) {
	s := &State{}

	if out, err := hideCmd("reg", "query", regPath, "/v", "ProxyEnable").CombinedOutput(); err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "ProxyEnable") {
				fields := strings.Fields(line)
				if len(fields) >= 3 {
					raw := strings.TrimPrefix(fields[2], "0x")
					if v, err := strconv.ParseUint(raw, 16, 32); err == nil {
						s.Enabled = v != 0
					}
				}
			}
		}
	}

	if out, err := hideCmd("reg", "query", regPath, "/v", "ProxyServer").CombinedOutput(); err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "ProxyServer") {
				fields := strings.Fields(line)
				if len(fields) >= 3 {
					s.Server = strings.Join(fields[2:], " ")
					s.HasServer = true
				}
			}
		}
	}

	return s, nil
}

// Set 开启系统代理并指向 server（如 "127.0.0.1:8899"）
func Set(server string) error {
	if err := hideCmd("reg", "add", regPath, "/v", "ProxyEnable",
		"/t", "REG_DWORD", "/d", "1", "/f").Run(); err != nil {
		return fmt.Errorf("set ProxyEnable: %w", err)
	}
	if err := hideCmd("reg", "add", regPath, "/v", "ProxyServer",
		"/t", "REG_SZ", "/d", server, "/f").Run(); err != nil {
		return fmt.Errorf("set ProxyServer: %w", err)
	}
	notifyChange()
	return nil
}

// Restore 还原到 Get() 时的快照
func Restore(s *State) error {
	if s == nil {
		return nil
	}
	enableVal := "0"
	if s.Enabled {
		enableVal = "1"
	}
	if err := hideCmd("reg", "add", regPath, "/v", "ProxyEnable",
		"/t", "REG_DWORD", "/d", enableVal, "/f").Run(); err != nil {
		return fmt.Errorf("restore ProxyEnable: %w", err)
	}
	if s.HasServer {
		if err := hideCmd("reg", "add", regPath, "/v", "ProxyServer",
			"/t", "REG_SZ", "/d", s.Server, "/f").Run(); err != nil {
			return fmt.Errorf("restore ProxyServer: %w", err)
		}
	} else {
		// 原本没设置过，删除我们写入的项
		_ = hideCmd("reg", "delete", regPath, "/v", "ProxyServer", "/f").Run()
	}
	notifyChange()
	return nil
}

// notifyChange 通知系统代理设置已变更，让 IE/Edge/WinINet 进程立即生效
func notifyChange() {
	const (
		INTERNET_OPTION_SETTINGS_CHANGED = 39
		INTERNET_OPTION_REFRESH          = 37
	)
	wininet := syscall.NewLazyDLL("wininet.dll")
	proc := wininet.NewProc("InternetSetOptionW")
	_, _, _ = proc.Call(0, INTERNET_OPTION_SETTINGS_CHANGED, 0, 0)
	_, _, _ = proc.Call(0, INTERNET_OPTION_REFRESH, 0, 0)
}
