// Package logger 负责记录抓包数据并持久化到日志文件（JSONL 格式）
package logger

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

// Entry 代表一条完整的抓包记录
type Entry struct {
	ID         int64             `json:"id"`
	Time       time.Time         `json:"time"`
	Type       string            `json:"type"`                // HTTP / HTTPS / WSS / WS_FRAME
	Direction  string            `json:"direction,omitempty"` // C→S / S→C（WebSocket 专用）
	Method     string            `json:"method"`
	URL        string            `json:"url"`
	Host       string            `json:"host"`
	Protocol   string            `json:"protocol"`
	ReqHeader  map[string]string `json:"req_header,omitempty"`
	ReqBody    string            `json:"req_body,omitempty"`
	StatusCode int               `json:"status_code,omitempty"`
	RespHeader map[string]string `json:"resp_header,omitempty"`
	RespBody   string            `json:"resp_body,omitempty"`
	Duration   int64             `json:"duration_ms,omitempty"`
}

// Logger 负责内存存储 + 文件落盘
type Logger struct {
	mu           sync.Mutex
	counter      int64
	logDir       string
	httpFile     *os.File
	wsFile       *os.File
	chessFile    *os.File
	errFile      *os.File // HTTP/HTTPS/WS 错误日志（不打印到终端）
	errLogger    *log.Logger
	curDate      string // 当前日期
	httpBytes    int64  // 已写字节数
	wsBytes      int64
	chessBytes   int64
	errBytes     int64
	maxSizeBytes int64 // 单文件最大字节（默认 20MB）
}

// ChessRecord 一条行棋记录（专供分析坐标系/字段含义）
type ChessRecord struct {
	ID        int64     `json:"id"`
	Time      time.Time `json:"time"`
	Direction string    `json:"direction"` // C→S 或 S→C
	Source    string    `json:"source"`    // chess_req_msg 或 chess_ack_msg
	MatchID   int64     `json:"matchid"`
	Seat      int       `json:"seat"` // 0=红 1=黑
	BeginPosX int       `json:"beginposx"`
	BeginPosY int       `json:"beginposy"`
	EndPosX   int       `json:"endposx"`
	EndPosY   int       `json:"endposy"`
	MoveType  int       `json:"movetype"` // 0=移动 1=吃子
	NextSeat  int       `json:"nextseat"`
	RoundTime int       `json:"roundtime"`
	RawJSON   string    `json:"raw_json"` // 原始 JSON 子串，便于核对其他字段
}

// New 创建 Logger，logDir 为日志目录，maxMB 为单文件上限（MB）
func New(logDir string, maxMB int64) *Logger {
	if err := os.MkdirAll(logDir, 0755); err != nil {
		log.Fatalf("创建日志目录失败: %v", err)
	}
	if maxMB <= 0 {
		maxMB = 20
	}
	l := &Logger{logDir: logDir, maxSizeBytes: maxMB * 1024 * 1024}
	l.openFiles() // 启动时强制清空
	return l
}

// openFiles 打开日志文件（清空模式）
func (l *Logger) openFiles() {
	today := time.Now().Format("2006-01-02")
	l.curDate = today

	if l.httpFile != nil {
		l.httpFile.Close()
	}
	if l.wsFile != nil {
		l.wsFile.Close()
	}

	httpPath := filepath.Join(l.logDir, fmt.Sprintf("http_%s.jsonl", today))
	wsPath := filepath.Join(l.logDir, fmt.Sprintf("ws_%s.jsonl", today))
	chessPath := filepath.Join(l.logDir, fmt.Sprintf("chess_%s.jsonl", today))
	errPath := filepath.Join(l.logDir, fmt.Sprintf("http_error_%s.log", today))

	if l.chessFile != nil {
		l.chessFile.Close()
	}
	if l.errFile != nil {
		l.errFile.Close()
	}

	var err error
	// O_TRUNC: 每次启动都清空文件
	l.httpFile, err = os.OpenFile(httpPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0644)
	if err != nil {
		log.Printf("打开 HTTP 日志文件失败: %v", err)
	}
	l.wsFile, err = os.OpenFile(wsPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0644)
	if err != nil {
		log.Printf("打开 WS 日志文件失败: %v", err)
	}
	l.chessFile, err = os.OpenFile(chessPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0644)
	if err != nil {
		log.Printf("打开行棋日志文件失败: %v", err)
	}
	l.errFile, err = os.OpenFile(errPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0644)
	if err != nil {
		log.Printf("打开 HTTP 错误日志文件失败: %v", err)
	} else {
		l.errLogger = log.New(l.errFile, "", log.LstdFlags)
	}
	l.httpBytes = 0
	l.wsBytes = 0
	l.chessBytes = 0
	l.errBytes = 0

	log.Printf("日志已清空并重建: %s | %s | %s | %s（上限 %dMB/文件）",
		httpPath, wsPath, chessPath, errPath, l.maxSizeBytes/1024/1024)
}

// Add 添加一条记录并写入对应日志文件
func (l *Logger) Add(e *Entry) {
	e.ID = atomic.AddInt64(&l.counter, 1)
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	// 自动填充 Type
	if e.Type == "" {
		switch {
		case e.Method == "WS-UPGRADE":
			e.Type = "WSS_HANDSHAKE"
		case e.Protocol == "WSS":
			e.Type = "WS_FRAME"
		default:
			e.Type = e.Protocol
		}
	}

	line, err := json.Marshal(e)
	if err != nil {
		return
	}
	line = append(line, '\n')
	lineLen := int64(len(line))

	l.mu.Lock()
	defer l.mu.Unlock()

	isWS := e.Type == "WS_FRAME" || e.Type == "WSS_HANDSHAKE"
	if isWS {
		if l.wsFile != nil {
			if l.wsBytes+lineLen > l.maxSizeBytes {
				// 超限则清空重写
				l.wsFile.Truncate(0)
				l.wsFile.Seek(0, 0)
				l.wsBytes = 0
				log.Printf("[logger] ws 日志已达上限，已自动清空")
			}
			l.wsFile.Write(line)
			l.wsBytes += lineLen
		}
	} else {
		if l.httpFile != nil {
			if l.httpBytes+lineLen > l.maxSizeBytes {
				l.httpFile.Truncate(0)
				l.httpFile.Seek(0, 0)
				l.httpBytes = 0
				log.Printf("[logger] http 日志已达上限，已自动清空")
			}
			l.httpFile.Write(line)
			l.httpBytes += lineLen
		}
	}
}

// AddChess 写入一条行棋记录到 chess_*.jsonl
func (l *Logger) AddChess(r *ChessRecord) {
	r.ID = atomic.AddInt64(&l.counter, 1)
	if r.Time.IsZero() {
		r.Time = time.Now()
	}
	line, err := json.Marshal(r)
	if err != nil {
		return
	}
	line = append(line, '\n')
	lineLen := int64(len(line))

	l.mu.Lock()
	defer l.mu.Unlock()
	if l.chessFile == nil {
		return
	}
	if l.chessBytes+lineLen > l.maxSizeBytes {
		l.chessFile.Truncate(0)
		l.chessFile.Seek(0, 0)
		l.chessBytes = 0
		log.Printf("[logger] chess 日志已达上限，已自动清空")
	}
	l.chessFile.Write(line)
	l.chessBytes += lineLen
}

// Close 关闭日志文件
func (l *Logger) Close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.httpFile != nil {
		l.httpFile.Close()
	}
	if l.wsFile != nil {
		l.wsFile.Close()
	}
	if l.chessFile != nil {
		l.chessFile.Close()
	}
	if l.errFile != nil {
		l.errFile.Close()
	}
}

// LogHTTPError 写入一条 HTTP/HTTPS/WS/CONNECT 相关错误到独立日志文件，
// 不输出到终端，避免干扰行棋分析输出。
func (l *Logger) LogHTTPError(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	line := fmt.Sprintf("%s %s\n", time.Now().Format("2006/01/02 15:04:05"), msg)
	lineLen := int64(len(line))

	l.mu.Lock()
	defer l.mu.Unlock()
	if l.errFile == nil {
		return
	}
	if l.errBytes+lineLen > l.maxSizeBytes {
		l.errFile.Truncate(0)
		l.errFile.Seek(0, 0)
		l.errBytes = 0
	}
	l.errFile.Write([]byte(line))
	l.errBytes += lineLen
}

// ---- 辅助函数 ----

// ReadHeaders 将 http.Header 转为 map[string]string
func ReadHeaders(h http.Header) map[string]string {
	m := make(map[string]string, len(h))
	for k, vs := range h {
		if len(vs) == 1 {
			m[k] = vs[0]
		} else {
			var buf bytes.Buffer
			for i, v := range vs {
				if i > 0 {
					buf.WriteString(", ")
				}
				buf.WriteString(v)
			}
			m[k] = buf.String()
		}
	}
	return m
}

// ReadBody 读取 body 内容并恢复 ReadCloser
func ReadBody(rc *io.ReadCloser, limit int64) string {
	if rc == nil || *rc == nil {
		return ""
	}
	if limit <= 0 {
		limit = 1 * 1024 * 1024
	}
	buf, err := io.ReadAll(io.LimitReader(*rc, limit))
	if err != nil {
		buf = []byte("[read error: " + err.Error() + "]")
	}
	*rc = io.NopCloser(bytes.NewReader(buf))
	return string(buf)
}
