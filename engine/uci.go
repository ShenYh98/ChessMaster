// Package engine 封装与 Pikafish 等 UCI 象棋引擎的进程通信
package engine

import (
	"bufio"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Engine 表示一个已启动的引擎子进程
type Engine struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	stdout  *bufio.Reader
	mu      sync.Mutex // 保护 stdin 写入和 stdout 读取（一次只能算一步）
	exePath string
	debug   bool
}

// Options 启动选项
type Options struct {
	ExePath  string // 引擎 exe 绝对/相对路径
	NNUEPath string // NNUE 权重文件路径，留空则使用引擎默认（同目录的 pikafish.nnue）
	Threads  int    // 线程数，<=0 表示不设置
	Hash     int    // 置换表大小 MB，<=0 表示不设置
	Debug    bool   // true 时打印引擎所有输出到 stderr
}

// Start 启动引擎子进程并完成 uci 初始化握手
func Start(opt Options) (*Engine, error) {
	if opt.ExePath == "" {
		return nil, fmt.Errorf("ExePath 不能为空")
	}
	if _, err := os.Stat(opt.ExePath); err != nil {
		return nil, fmt.Errorf("引擎可执行文件不存在: %s (%v)", opt.ExePath, err)
	}

	// 转为绝对路径，避免 Windows 下相对路径启动子进程时被当作 PATH 查找而失败
	absExe, err := filepath.Abs(opt.ExePath)
	if err != nil {
		return nil, fmt.Errorf("转绝对路径失败: %w", err)
	}

	cmd := exec.Command(absExe)
	cmd.Dir = filepath.Dir(absExe) // 设工作目录，让引擎从同目录加载默认 nnue

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("StdinPipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("StdoutPipe: %w", err)
	}
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("启动引擎进程失败: %w", err)
	}

	e := &Engine{
		cmd:     cmd,
		stdin:   stdin,
		stdout:  bufio.NewReader(stdout),
		exePath: opt.ExePath,
		debug:   opt.Debug,
	}

	// uci 握手
	if err := e.sendLocked("uci"); err != nil {
		_ = e.Stop()
		return nil, fmt.Errorf("发送 uci 失败: %w", err)
	}
	if err := e.waitForLocked("uciok", 10*time.Second); err != nil {
		_ = e.Stop()
		return nil, fmt.Errorf("等待 uciok 超时: %w", err)
	}

	// 设置选项
	if opt.NNUEPath != "" {
		_ = e.sendLocked(fmt.Sprintf("setoption name EvalFile value %s", opt.NNUEPath))
	}
	if opt.Threads > 0 {
		_ = e.sendLocked(fmt.Sprintf("setoption name Threads value %d", opt.Threads))
	}
	if opt.Hash > 0 {
		_ = e.sendLocked(fmt.Sprintf("setoption name Hash value %d", opt.Hash))
	}

	// isready
	if err := e.sendLocked("isready"); err != nil {
		_ = e.Stop()
		return nil, fmt.Errorf("发送 isready 失败: %w", err)
	}
	if err := e.waitForLocked("readyok", 10*time.Second); err != nil {
		_ = e.Stop()
		return nil, fmt.Errorf("等待 readyok 超时: %w", err)
	}

	return e, nil
}

// sendLocked 写一行命令到引擎（不加锁，由调用者保证）
func (e *Engine) sendLocked(cmd string) error {
	if e.debug {
		log.Printf("[engine ←] %s", cmd)
	}
	_, err := io.WriteString(e.stdin, cmd+"\n")
	return err
}

// readLineLocked 读一行（不加锁）
func (e *Engine) readLineLocked() (string, error) {
	line, err := e.stdout.ReadString('\n')
	if err != nil {
		return "", err
	}
	line = strings.TrimRight(line, "\r\n")
	if e.debug {
		log.Printf("[engine →] %s", line)
	}
	return line, nil
}

// waitForLocked 持续读行直到出现包含 token 的行（不加锁）
func (e *Engine) waitForLocked(token string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if time.Now().After(deadline) {
			return fmt.Errorf("timeout waiting for %q", token)
		}
		line, err := e.readLineLocked()
		if err != nil {
			return err
		}
		if strings.Contains(line, token) {
			return nil
		}
	}
}

// BestMove 给定 FEN 局面，让引擎思考 movetimeMs 毫秒后返回最佳着法（如 "h2e2"）
// 着法格式：UCI 风格的 起点列字母+起点行 → 终点列字母+终点行
// 中国象棋列 a-i (从红方左到右), 行 0-9 (红方底线为 0)
func (e *Engine) BestMove(fen string, movetimeMs int) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if err := e.sendLocked("position fen " + fen); err != nil {
		return "", err
	}
	if err := e.sendLocked(fmt.Sprintf("go movetime %d", movetimeMs)); err != nil {
		return "", err
	}

	timeout := time.Duration(movetimeMs)*time.Millisecond + 10*time.Second
	deadline := time.Now().Add(timeout)
	for {
		if time.Now().After(deadline) {
			return "", fmt.Errorf("等待 bestmove 超时")
		}
		line, err := e.readLineLocked()
		if err != nil {
			return "", err
		}
		if strings.HasPrefix(line, "bestmove ") {
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				return parts[1], nil
			}
			return "", fmt.Errorf("无效 bestmove 行: %q", line)
		}
	}
}

// Stop 优雅关闭引擎
func (e *Engine) Stop() error {
	e.mu.Lock()
	defer e.mu.Unlock()

	_ = e.sendLocked("quit")
	_ = e.stdin.Close()

	done := make(chan error, 1)
	go func() { done <- e.cmd.Wait() }()
	select {
	case err := <-done:
		return err
	case <-time.After(3 * time.Second):
		_ = e.cmd.Process.Kill()
		return <-done
	}
}
