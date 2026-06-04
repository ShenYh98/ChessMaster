// Package webui 提供 Web 实时棋盘可视化（HTTP + Server-Sent Events）
package webui

import (
	"chessmaster/engine"
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"sync"
	"time"
)

//go:embed assets/*
var assetsFS embed.FS

// Server 是 Web 可视化服务
type Server struct {
	advisor *engine.Advisor
	addr    string

	mu      sync.Mutex
	clients map[chan []byte]struct{}

	httpServer *http.Server
}

// New 创建一个 Web 服务实例
func New(advisor *engine.Advisor, addr string) *Server {
	return &Server{
		advisor: advisor,
		addr:    addr,
		clients: make(map[chan []byte]struct{}),
	}
}

// Start 在后台启动 HTTP 服务
func (s *Server) Start() error {
	mux := http.NewServeMux()

	sub, err := fs.Sub(assetsFS, "assets")
	if err != nil {
		return fmt.Errorf("embed sub: %w", err)
	}
	mux.Handle("/", http.FileServer(http.FS(sub)))
	mux.HandleFunc("/events", s.handleEvents)
	mux.HandleFunc("/snapshot", s.handleSnapshot)

	s.httpServer = &http.Server{
		Addr:    s.addr,
		Handler: mux,
	}

	go func() {
		if err := s.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("[webui] 服务异常退出: %v", err)
		}
	}()
	return nil
}

// Shutdown 优雅关闭
func (s *Server) Shutdown(ctx context.Context) error {
	if s.httpServer == nil {
		return nil
	}
	// 关闭所有 SSE 客户端通道
	s.mu.Lock()
	for ch := range s.clients {
		close(ch)
		delete(s.clients, ch)
	}
	s.mu.Unlock()
	return s.httpServer.Shutdown(ctx)
}

// Broadcast 广播一次最新快照（advisor 状态变更时调用）
func (s *Server) Broadcast() {
	if s.advisor == nil {
		return
	}
	snap := s.advisor.Snapshot()
	data, err := json.Marshal(snap)
	if err != nil {
		return
	}
	s.mu.Lock()
	for ch := range s.clients {
		// 非阻塞发送，慢客户端跳过
		select {
		case ch <- data:
		default:
		}
	}
	s.mu.Unlock()
}

// handleSnapshot 一次性返回当前快照（便于调试）
func (s *Server) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if s.advisor == nil {
		_, _ = w.Write([]byte(`{"active":false}`))
		return
	}
	_ = json.NewEncoder(w).Encode(s.advisor.Snapshot())
}

// handleEvents Server-Sent Events 端点
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	ch := make(chan []byte, 4)
	s.mu.Lock()
	s.clients[ch] = struct{}{}
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		if _, ok := s.clients[ch]; ok {
			delete(s.clients, ch)
		}
		s.mu.Unlock()
	}()

	// 连接建立时立刻推一次当前快照
	if s.advisor != nil {
		if data, err := json.Marshal(s.advisor.Snapshot()); err == nil {
			_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		}
	}

	// 心跳定时器（防止代理断连）
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	notify := r.Context().Done()
	for {
		select {
		case <-notify:
			return
		case data, ok := <-ch:
			if !ok {
				return
			}
			_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		case <-ticker.C:
			_, _ = fmt.Fprintf(w, ": ping\n\n")
			flusher.Flush()
		}
	}
}
