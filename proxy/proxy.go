// Package proxy 实现 HTTP/HTTPS MITM 代理核心逻辑
package proxy

import (
	"bufio"
	"bytes"
	"chessmaster/cert"
	"chessmaster/engine"
	"chessmaster/logger"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// hop-by-hop 头不应被转发（WebSocket 升级时保留 Upgrade/Connection）
var hopByHopHeaders = []string{
	"Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization",
	"Proxy-Connection", "TE", "Trailers", "Transfer-Encoding",
}

// isWebSocket 判断是否为 WebSocket 升级请求
func isWebSocket(h http.Header) bool {
	return strings.EqualFold(h.Get("Upgrade"), "websocket")
}

// Proxy 是 MITM 代理服务器
type Proxy struct {
	ca      *cert.CA
	logger  *logger.Logger
	client  *http.Client
	advisor *engine.Advisor // 可为 nil（未启用引擎推荐时）
}

// New 创建代理实例
func New(ca *cert.CA, l *logger.Logger) *Proxy {
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: false,
			MinVersion:         tls.VersionTLS12,
		},
		// 强制只用 HTTP/1.1，禁止 HTTP/2（避免响应序列化错乱）
		TLSNextProto: make(map[string]func(authority string, c *tls.Conn) http.RoundTripper),
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ForceAttemptHTTP2:     false,
	}
	return &Proxy{
		ca:     ca,
		logger: l,
		client: &http.Client{
			Transport: transport,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
			Timeout: 60 * time.Second,
		},
	}
}

// SetAdvisor 注入行棋推荐器（可选）
func (p *Proxy) SetAdvisor(a *engine.Advisor) {
	p.advisor = a
}

// removeHopByHop 移除 hop-by-hop 头（WebSocket 请求保留 Upgrade/Connection）
func removeHopByHop(h http.Header) {
	if isWebSocket(h) {
		// WebSocket 升级：只删代理专用头，保留 Upgrade/Connection/Sec-WebSocket-*
		h.Del("Proxy-Connection")
		h.Del("Proxy-Authenticate")
		h.Del("Proxy-Authorization")
		return
	}
	// 普通 HTTP：处理 Connection 头指定的字段
	if conn := h.Get("Connection"); conn != "" {
		for _, f := range strings.Split(conn, ",") {
			h.Del(strings.TrimSpace(f))
		}
	}
	for _, hdr := range hopByHopHeaders {
		h.Del(hdr)
	}
	h.Del("Connection")
	h.Del("Upgrade")
}

// ServeHTTP 处理所有代理请求
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		p.handleConnect(w, r)
	} else {
		p.handleHTTP(w, r)
	}
}

// handleHTTP 处理普通 HTTP 请求
func (p *Proxy) handleHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()

	reqBody := logger.ReadBody(&r.Body, 1*1024*1024)
	reqHeaders := logger.ReadHeaders(r.Header)

	outReq := r.Clone(r.Context())
	outReq.RequestURI = ""
	removeHopByHop(outReq.Header)

	resp, err := p.client.Do(outReq)
	if err != nil {
		http.Error(w, fmt.Sprintf("proxy error: %v", err), http.StatusBadGateway)
		p.logger.Add(&logger.Entry{
			Time: start, Method: r.Method, URL: r.URL.String(),
			Host: r.Host, Protocol: "HTTP",
			ReqHeader: reqHeaders, ReqBody: reqBody,
			Duration: time.Since(start).Milliseconds(),
		})
		return
	}
	defer resp.Body.Close()

	respBodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 1*1024*1024))
	respBody := string(respBodyBytes)
	respHeaders := logger.ReadHeaders(resp.Header)

	// 透传响应头（去掉 hop-by-hop）
	removeHopByHop(resp.Header)
	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	w.Write(respBodyBytes)

	p.logger.Add(&logger.Entry{
		Time: start, Method: r.Method, URL: r.URL.String(),
		Host: r.Host, Protocol: "HTTP",
		ReqHeader: reqHeaders, ReqBody: reqBody,
		StatusCode: resp.StatusCode,
		RespHeader: respHeaders, RespBody: respBody,
		Duration: time.Since(start).Milliseconds(),
	})
}

// handleConnect 处理 HTTPS CONNECT 隧道，执行 MITM
func (p *Proxy) handleConnect(w http.ResponseWriter, r *http.Request) {
	host := r.Host
	hostname := host
	if h, _, err := net.SplitHostPort(host); err == nil {
		hostname = h
	}

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijacking not supported", http.StatusInternalServerError)
		return
	}
	clientConn, bufrw, err := hijacker.Hijack()
	if err != nil {
		p.logger.LogHTTPError("[CONNECT] hijack error: %v", err)
		return
	}
	_, _ = clientConn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))

	// 为该域名动态签发证书
	tlsCfg, err := p.ca.TLSConfig(hostname)
	if err != nil {
		p.logger.LogHTTPError("[CONNECT] TLS config error for %s: %v", hostname, err)
		clientConn.Close()
		return
	}
	// 宽松的 TLS 服务端配置，兼容更多客户端
	tlsCfg.MinVersion = tls.VersionTLS10
	tlsCfg.MaxVersion = tls.VersionTLS13

	// 如果 bufio 缓冲区有剩余数据，需要合并回连接
	var rawConn net.Conn = clientConn
	if bufrw.Reader.Buffered() > 0 {
		buffered := make([]byte, bufrw.Reader.Buffered())
		bufrw.Reader.Read(buffered)
		rawConn = &bufferedConn{Conn: clientConn, buf: bytes.NewReader(buffered)}
	}

	tlsClientConn := tls.Server(rawConn, tlsCfg)
	if err := tlsClientConn.SetDeadline(time.Now().Add(10 * time.Second)); err == nil {
		if err := tlsClientConn.Handshake(); err != nil {
			p.logger.LogHTTPError("[CONNECT] TLS handshake error for %s: %v", hostname, err)
			tlsClientConn.Close()
			return
		}
		tlsClientConn.SetDeadline(time.Time{}) // 清除 deadline
	}

	go p.handleTLSConn(tlsClientConn, host)
}

// bufferedConn 将缓冲数据与底层连接合并
type bufferedConn struct {
	net.Conn
	buf *bytes.Reader
}

func (c *bufferedConn) Read(b []byte) (int, error) {
	if c.buf.Len() > 0 {
		return c.buf.Read(b)
	}
	return c.Conn.Read(b)
}

func (p *Proxy) handleTLSConn(conn *tls.Conn, originalHost string) {
	defer conn.Close()
	reader := bufio.NewReader(conn)
	for {
		conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		req, err := http.ReadRequest(reader)
		if err != nil {
			if err != io.EOF &&
				!strings.Contains(err.Error(), "connection reset") &&
				!strings.Contains(err.Error(), "use of closed") &&
				!strings.Contains(err.Error(), "timeout") {
				p.logger.LogHTTPError("[HTTPS] read request error from %s: %v", originalHost, err)
			}
			return
		}
		conn.SetReadDeadline(time.Time{})

		req.URL.Host = originalHost
		req.URL.Scheme = "https"
		req.RequestURI = ""
		if req.Host == "" {
			req.Host = originalHost
		}

		// WebSocket 升级请求：直接建立透明隧道转发，不做 MITM 解析
		if isWebSocket(req.Header) {
			p.handleWebSocket(conn, reader, req, originalHost)
			return
		}

		removeHopByHop(req.Header)

		start := time.Now()
		reqBody := logger.ReadBody(&req.Body, 1*1024*1024)
		reqHeaders := logger.ReadHeaders(req.Header)

		resp, err := p.client.Do(req)
		if err != nil {
			p.logger.LogHTTPError("[HTTPS] upstream error %s %s: %v", req.Method, req.URL.String(), err)
			p.logger.Add(&logger.Entry{
				Time: start, Method: req.Method, URL: req.URL.String(),
				Host: originalHost, Protocol: "HTTPS",
				ReqHeader: reqHeaders, ReqBody: reqBody,
				Duration: time.Since(start).Milliseconds(),
			})
			// 写回 502，让客户端知道出错而非静默断开
			errResp := &http.Response{
				Status:     "502 Bad Gateway",
				StatusCode: http.StatusBadGateway,
				Proto:      "HTTP/1.1",
				ProtoMajor: 1, ProtoMinor: 1,
				Header:        make(http.Header),
				Body:          io.NopCloser(strings.NewReader(err.Error())),
				ContentLength: int64(len(err.Error())),
			}
			errResp.Header.Set("Content-Type", "text/plain")
			_ = errResp.Write(conn)
			return // 出错后断开，避免死循环
		}

		// 读取响应体（先缓存，再回写）
		respBodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 1*1024*1024))
		resp.Body.Close()
		respBody := string(respBodyBytes)
		respHeaders := logger.ReadHeaders(resp.Header)

		p.logger.Add(&logger.Entry{
			Time: start, Method: req.Method, URL: req.URL.String(),
			Host: originalHost, Protocol: "HTTPS",
			ReqHeader: reqHeaders, ReqBody: reqBody,
			StatusCode: resp.StatusCode,
			RespHeader: respHeaders, RespBody: respBody,
			Duration: time.Since(start).Milliseconds(),
		})

		// 手动构造 HTTP/1.1 响应写回客户端（避免 Transfer-Encoding 错乱）
		removeHopByHop(resp.Header)
		resp.Header.Set("Content-Length", fmt.Sprintf("%d", len(respBodyBytes)))
		resp.Header.Del("Transfer-Encoding")
		resp.TransferEncoding = nil
		resp.ContentLength = int64(len(respBodyBytes))
		resp.Body = io.NopCloser(bytes.NewReader(respBodyBytes))
		resp.Proto = "HTTP/1.1"
		resp.ProtoMajor = 1
		resp.ProtoMinor = 1

		if err := resp.Write(conn); err != nil {
			p.logger.LogHTTPError("[HTTPS] write response error: %v", err)
			return
		}

		// 连接关闭判断
		if req.ProtoMajor == 1 && req.ProtoMinor == 0 {
			return
		}
		if strings.EqualFold(resp.Header.Get("Connection"), "close") {
			return
		}
	}
}

// handleWebSocket 对 WebSocket 升级请求做透明隧道转发，同时记录握手信息
func (p *Proxy) handleWebSocket(clientConn net.Conn, clientReader *bufio.Reader, req *http.Request, originalHost string) {
	start := time.Now()
	reqHeaders := logger.ReadHeaders(req.Header)

	// 连接真实服务器
	hostname, port, err := net.SplitHostPort(originalHost)
	if err != nil {
		hostname = originalHost
		port = "443"
	}
	upstreamConn, err := tls.Dial("tcp", net.JoinHostPort(hostname, port), &tls.Config{
		InsecureSkipVerify: false,
		ServerName:         hostname,
		MinVersion:         tls.VersionTLS12,
	})
	if err != nil {
		p.logger.LogHTTPError("[WS] upstream dial error %s: %v", originalHost, err)
		errResp := "HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\n\r\n"
		clientConn.Write([]byte(errResp))
		p.logger.Add(&logger.Entry{
			Time: start, Method: "WS-UPGRADE", URL: "wss://" + originalHost + req.URL.RequestURI(),
			Host: originalHost, Protocol: "WSS",
			ReqHeader: reqHeaders, RespBody: "upstream dial error: " + err.Error(),
			Duration: time.Since(start).Milliseconds(),
		})
		return
	}
	defer upstreamConn.Close()

	// 转发升级请求到真实服务器
	if err := req.Write(upstreamConn); err != nil {
		p.logger.LogHTTPError("[WS] write upgrade request error: %v", err)
		return
	}

	// 读取服务器的握手响应
	upstreamReader := bufio.NewReader(upstreamConn)
	resp, err := http.ReadResponse(upstreamReader, req)
	if err != nil {
		p.logger.LogHTTPError("[WS] read upgrade response error: %v", err)
		return
	}

	// 记录 WebSocket 握手
	p.logger.Add(&logger.Entry{
		Time: start, Method: "WS-UPGRADE", URL: "wss://" + originalHost + req.URL.RequestURI(),
		Host: originalHost, Protocol: "WSS",
		ReqHeader:  reqHeaders,
		StatusCode: resp.StatusCode,
		RespHeader: logger.ReadHeaders(resp.Header),
		RespBody:   fmt.Sprintf("WebSocket 隧道已建立 (101 Switching Protocols)"),
		Duration:   time.Since(start).Milliseconds(),
	})
	// WS 隧道建立成功

	// 将握手响应写回客户端
	if err := resp.Write(clientConn); err != nil {
		p.logger.LogHTTPError("[WS] write upgrade response to client error: %v", err)
		return
	}
	resp.Body.Close()

	// 双向帧级别转发 + 日志记录
	// 使用 bufio reader 以确保握手阶段已缓冲的数据不丢失
	wsURL := "wss://" + originalHost + req.URL.RequestURI()
	done := make(chan struct{}, 2)
	go p.wsRelayWithLog(clientReader, upstreamConn, wsURL, originalHost, "C→S", done)
	go p.wsRelayWithLog(upstreamReader, clientConn, wsURL, originalHost, "S→C", done)
	<-done
}
