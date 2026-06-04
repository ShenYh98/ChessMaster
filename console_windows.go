//go:build windows

// 注册 Windows 控制台事件处理器，确保用户点击控制台 X 关闭、注销、关机时
// 也能"先于程序被强杀之前"还原系统代理并关闭浏览器子进程。
//
// 背景：
//   - Ctrl+C / Ctrl+Break → CTRL_C_EVENT，Go 标准库已映射为 SIGINT
//   - 控制台 X 关闭     → CTRL_CLOSE_EVENT，Windows 仅给约 5 秒清理时间
//   - 用户注销 / 关机   → CTRL_LOGOFF_EVENT / CTRL_SHUTDOWN_EVENT
//
// 当我们的清理动作（reg add 还原代理、taskkill 关浏览器、HTTP Server.Shutdown 超时）
// 总耗时可能超过 5 秒时，常规 defer / signal 流程会被 Windows 强杀打断。
// 因此把"关键清理"放进 console handler 同步执行，保证一定在进程被终止前完成。

package main

import (
	"sync"
	"syscall"
)

const (
	_CTRL_C_EVENT        = 0
	_CTRL_BREAK_EVENT    = 1
	_CTRL_CLOSE_EVENT    = 2
	_CTRL_LOGOFF_EVENT   = 5
	_CTRL_SHUTDOWN_EVENT = 6
)

var (
	consoleCleanupFn   func()
	consoleCleanupOnce sync.Once
	// 持有回调，避免被 GC 回收（NewCallback 注册的指针生命周期需保持到进程结束）
	consoleCallback uintptr
)

// setupConsoleCleanup 注册一个 Windows 控制台事件处理器。
// 收到任何控制台终止类事件时，同步执行 cleanup（仅一次），然后返回 FALSE
// 让事件继续传给 Go 运行时注册的 handler，触发 SIGINT/SIGTERM，main 走常规退出流程。
func setupConsoleCleanup(cleanup func()) {
	consoleCleanupFn = cleanup
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	proc := kernel32.NewProc("SetConsoleCtrlHandler")
	consoleCallback = syscall.NewCallback(consoleHandler)
	// 第二个参数 Add=1 表示添加 handler；handler 链按 LIFO 顺序执行，
	// 我们后注册的会比 Go runtime 的更早调用，刚好用来"抢跑"做关键清理。
	_, _, _ = proc.Call(consoleCallback, 1)
}

// consoleHandler 是 Windows 控制台事件回调
// ctrlType 见 CTRL_*_EVENT 常量；返回 0 (FALSE) 表示让后续 handler 继续处理。
func consoleHandler(ctrlType uint32) uintptr {
	switch ctrlType {
	case _CTRL_C_EVENT, _CTRL_BREAK_EVENT,
		_CTRL_CLOSE_EVENT, _CTRL_LOGOFF_EVENT, _CTRL_SHUTDOWN_EVENT:
		if consoleCleanupFn != nil {
			consoleCleanupOnce.Do(consoleCleanupFn)
		}
	}
	// 返回 FALSE：让 Go runtime 注册的 handler 接着处理，
	// 这样 main goroutine 中 <-quit 仍能收到 SIGINT/SIGTERM 走常规退出流程
	return 0
}
