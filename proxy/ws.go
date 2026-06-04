// Package proxy - WebSocket 帧解析与双向抗包
package proxy

import (
	"chessmaster/engine"
	"chessmaster/logger"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"strings"
	"time"
	"unicode/utf8"
)

// ---- 象棋行棋解析 ----

type chessMoveMsg struct {
	ChessAckMsg *struct {
		MatchID int64            `json:"matchid"`
		Move    *chessMoveFields `json:"chessmove_ack_msg"`
	} `json:"chess_ack_msg"`
	ChessReqMsg *struct {
		MatchID int64            `json:"matchid"`
		Move    *chessMoveFields `json:"chessmove_req_msg"`
	} `json:"chess_req_msg"`
}

type chessMoveFields struct {
	BeginPosX int `json:"beginposx"`
	BeginPosY int `json:"beginposy"`
	EndPosX   int `json:"endposx"`
	EndPosY   int `json:"endposy"`
	Seat      int `json:"seat"`
	MoveType  int `json:"movetype"`
	NextSeat  int `json:"nextseat"`
	RoundTime int `json:"roundtime"`
}

// gameEventMsg 解析对局生命周期事件（服务器推送）
type gameEventMsg struct {
	ChessAckMsg *struct {
		MatchID  int64 `json:"matchid"`
		SetColor *struct {
			RedSeat int `json:"redseat"`
		} `json:"chesssetcolor_ack_msg"`
		GameOver *struct {
			Seat int    `json:"seat"`
			Text string `json:"text"`
		} `json:"chessgameover_ack_msg"`
	} `json:"chess_ack_msg"`
	MatchAckMsg *struct {
		MatchID    int64 `json:"matchid"`
		EnterMatch *struct {
			MatchID   int64  `json:"matchid"`
			MatchName string `json:"matchname"`
		} `json:"entermatch_ack_msg"`
		EnterRound *struct {
			SeatOrder int `json:"seatorder"`
		} `json:"enterround_ack_msg"`
	} `json:"match_ack_msg"`
}

// clientEventMsg 客户端发起的对局事件（退出 / 认输 等）
type clientEventMsg struct {
	MatchReqMsg *struct {
		MatchID int64                  `json:"matchid"`
		Leave   map[string]interface{} `json:"leave_req_msg"`
		Exit    map[string]interface{} `json:"exitmatch_req_msg"`
	} `json:"match_req_msg"`
}

// detectGameEvents 从服务器推送中识别对局事件并通知 advisor
func detectGameEvents(body string, advisor *engine.Advisor) {
	if advisor == nil {
		return
	}
	start := strings.Index(body, "{")
	if start < 0 {
		return
	}
	jsonStr := body[start:]
	if idx := strings.Index(jsonStr, "\n\n原始:"); idx >= 0 {
		jsonStr = strings.TrimSpace(jsonStr[:idx])
	}
	var msg gameEventMsg
	if err := json.Unmarshal([]byte(jsonStr), &msg); err != nil {
		return
	}
	if msg.MatchAckMsg != nil {
		if em := msg.MatchAckMsg.EnterMatch; em != nil {
			advisor.OnGameStart(em.MatchID, em.MatchName)
		}
		if er := msg.MatchAckMsg.EnterRound; er != nil {
			advisor.SetMySeatOrder(er.SeatOrder)
		}
	}
	if msg.ChessAckMsg != nil {
		if sc := msg.ChessAckMsg.SetColor; sc != nil {
			advisor.SetRedSeat(sc.RedSeat)
		}
		if over := msg.ChessAckMsg.GameOver; over != nil {
			advisor.OnGameOver(over.Seat, over.Text)
		}
	}
}

// detectClientEvents 从客户端发出中识别退出 / 认输 等事件
func detectClientEvents(body string, advisor *engine.Advisor) {
	if advisor == nil {
		return
	}
	start := strings.Index(body, "{")
	if start < 0 {
		return
	}
	jsonStr := body[start:]
	if idx := strings.Index(jsonStr, "\n\n原始:"); idx >= 0 {
		jsonStr = strings.TrimSpace(jsonStr[:idx])
	}
	var msg clientEventMsg
	if err := json.Unmarshal([]byte(jsonStr), &msg); err != nil {
		return
	}
	if msg.MatchReqMsg == nil {
		return
	}
	if msg.MatchReqMsg.Leave != nil {
		advisor.OnGameLeave(msg.MatchReqMsg.MatchID, "离开对局")
	}
	if msg.MatchReqMsg.Exit != nil {
		advisor.OnGameLeave(msg.MatchReqMsg.MatchID, "退出对局")
	}
}

// printChessMoveIfMatch 解析字符串中的 JSON，若包含行棋信息则写入行棋日志并打印服务器确认包到终端
func printChessMoveIfMatch(body, direction string, lg *logger.Logger, advisor *engine.Advisor) {
	// 找到第一个 '{'，取子串尝试解析
	start := strings.Index(body, "{")
	if start < 0 {
		return
	}
	jsonStr := body[start:]
	// 如果包含 "\n原始:" 则只取前半段
	if idx := strings.Index(jsonStr, "\n\n原始:"); idx >= 0 {
		jsonStr = strings.TrimSpace(jsonStr[:idx])
	}

	var msg chessMoveMsg
	if err := json.Unmarshal([]byte(jsonStr), &msg); err != nil {
		return
	}

	var move *chessMoveFields
	var matchID int64
	var source string
	var isAck bool

	if msg.ChessAckMsg != nil && msg.ChessAckMsg.Move != nil {
		move = msg.ChessAckMsg.Move
		matchID = msg.ChessAckMsg.MatchID
		source = "chess_ack_msg"
		isAck = true
	} else if msg.ChessReqMsg != nil && msg.ChessReqMsg.Move != nil {
		move = msg.ChessReqMsg.Move
		matchID = msg.ChessReqMsg.MatchID
		source = "chess_req_msg"
	}
	if move == nil {
		return
	}

	// 1) 写入行棋专用日志（req 和 ack 都记，便于分析坐标系）
	if lg != nil {
		lg.AddChess(&logger.ChessRecord{
			Direction: direction,
			Source:    source,
			MatchID:   matchID,
			Seat:      move.Seat,
			BeginPosX: move.BeginPosX,
			BeginPosY: move.BeginPosY,
			EndPosX:   move.EndPosX,
			EndPosY:   move.EndPosY,
			MoveType:  move.MoveType,
			NextSeat:  move.NextSeat,
			RoundTime: move.RoundTime,
			RawJSON:   jsonStr,
		})
	}

	// 2) 只打印服务器回包到终端，避免请求和回包重复
	if !isAck {
		return
	}

	// 3) 通知 advisor（只用服务器确认的 ack，避免棋盘状态错乱）
	if advisor != nil {
		advisor.OnMove(engine.MoveInfo{
			MatchID:  matchID,
			Seat:     move.Seat,
			X1:       move.BeginPosX,
			Y1:       move.BeginPosY,
			X2:       move.EndPosX,
			Y2:       move.EndPosY,
			NextSeat: move.NextSeat,
			MoveType: move.MoveType,
		})
	}

	player := seatColorName(advisor, move.Seat)
	action := "移动"
	if move.MoveType == 1 {
		action = "吃子"
	}
	nextPlayer := seatColorName(advisor, move.NextSeat)
	myColor := myColorForDisplay(advisor)
	dx1, dy1 := engine.DisplayXY(move.BeginPosX, move.BeginPosY, myColor)
	dx2, dy2 := engine.DisplayXY(move.EndPosX, move.EndPosY, myColor)
	log.Printf("[行棋] [%d] %-3s %s  (%d,%d) -> (%d,%d)  用时:%ds  下一手轮到%s",
		matchID, player, action,
		dx1, dy1, dx2, dy2,
		move.RoundTime, nextPlayer,
	)
}

// myColorForDisplay 从 advisor 安全取出我方颜色（不是席位！），nil 时返回 -1
func myColorForDisplay(advisor *engine.Advisor) int {
	if advisor == nil {
		return -1
	}
	return advisor.MyColor()
}

// seatColorName 根据 advisor 的 redSeat 判断 seat 对应「红方」或「黑方」
func seatColorName(advisor *engine.Advisor, seat int) string {
	if advisor != nil {
		return advisor.SeatColorName(seat)
	}
	// nil 时回退
	if seat == 1 {
		return "红方"
	}
	return "黑方"
}

const (
	wsOpcodeContinue = 0x0
	wsOpcodeText     = 0x1
	wsOpcodeBinary   = 0x2
	wsOpcodeClose    = 0x8
	wsOpcodePing     = 0x9
	wsOpcodePong     = 0xA

	wsMaxLogPayload = 64 * 1024 // 单帧最大记录 64KB
)

type wsFrame struct {
	Fin     bool
	Opcode  byte
	Masked  bool
	Payload []byte // 已解掩码，用于日志显示
	Raw     []byte // 原始字节，用于透传
}

func opcodeStr(op byte) string {
	switch op {
	case wsOpcodeText:
		return "TEXT"
	case wsOpcodeBinary:
		return "BINARY"
	case wsOpcodeClose:
		return "CLOSE"
	case wsOpcodePing:
		return "PING"
	case wsOpcodePong:
		return "PONG"
	case wsOpcodeContinue:
		return "CONT"
	default:
		return fmt.Sprintf("0x%X", op)
	}
}

// readWSFrame 从 reader 读取一个完整的 WebSocket 帧
func readWSFrame(r io.Reader) (*wsFrame, error) {
	header := make([]byte, 2)
	if _, err := io.ReadFull(r, header); err != nil {
		return nil, err
	}

	fin := (header[0] & 0x80) != 0
	opcode := header[0] & 0x0F
	masked := (header[1] & 0x80) != 0
	payloadLen := int64(header[1] & 0x7F)

	raw := append([]byte{}, header...)

	// 扩展长度
	switch payloadLen {
	case 126:
		ext := make([]byte, 2)
		if _, err := io.ReadFull(r, ext); err != nil {
			return nil, err
		}
		payloadLen = int64(binary.BigEndian.Uint16(ext))
		raw = append(raw, ext...)
	case 127:
		ext := make([]byte, 8)
		if _, err := io.ReadFull(r, ext); err != nil {
			return nil, err
		}
		payloadLen = int64(binary.BigEndian.Uint64(ext))
		raw = append(raw, ext...)
	}

	// 掩码键
	var maskKey [4]byte
	if masked {
		if _, err := io.ReadFull(r, maskKey[:]); err != nil {
			return nil, err
		}
		raw = append(raw, maskKey[:]...)
	}

	// 限制读取大小，防止内存溢出
	readLen := payloadLen
	if readLen > wsMaxLogPayload {
		readLen = wsMaxLogPayload
	}
	payload := make([]byte, payloadLen)
	if _, err := io.ReadFull(r, payload); err != nil {
		return nil, err
	}
	raw = append(raw, payload...)

	// 解掩码（用于日志显示）
	decoded := make([]byte, len(payload))
	copy(decoded, payload)
	if masked {
		for i := range decoded {
			decoded[i] ^= maskKey[i%4]
		}
	}

	return &wsFrame{
		Fin:     fin,
		Opcode:  opcode,
		Masked:  masked,
		Payload: decoded[:readLen],
		Raw:     raw,
	}, nil
}

// payloadStr 将帧 payload 转为可读字符串，自动尝试解析二进制协议
func payloadStr(f *wsFrame) string {
	switch f.Opcode {
	case wsOpcodeText:
		s := string(f.Payload)
		if !utf8.ValidString(s) {
			return fmt.Sprintf("[invalid utf8, %d bytes]", len(f.Payload))
		}
		return prettyJSON(s)
	case wsOpcodeBinary:
		return decodeBinaryFrame(f.Payload)
	case wsOpcodeClose:
		if len(f.Payload) >= 2 {
			code := binary.BigEndian.Uint16(f.Payload[:2])
			reason := ""
			if len(f.Payload) > 2 {
				reason = string(f.Payload[2:])
			}
			return fmt.Sprintf("[CLOSE code=%d reason=%q]", code, reason)
		}
		return "[CLOSE]"
	case wsOpcodePing:
		return "[PING]"
	case wsOpcodePong:
		return "[PONG]"
	default:
		return fmt.Sprintf("[opcode=%s %d bytes]", opcodeStr(f.Opcode), len(f.Payload))
	}
}

// decodeBinaryFrame 尝试解析二进制帧
// 支持协议格式：[4字节标志][4字节JSON长度(小端)][JSON]
func decodeBinaryFrame(data []byte) string {
	if len(data) < 8 {
		return fmt.Sprintf("[binary %d bytes] %X", len(data), data)
	}

	// 尝试 [4字节标志 + 4字节小端长度 + JSON] 格式
	jsonLen := int(binary.LittleEndian.Uint32(data[4:8]))
	if jsonLen > 0 && 8+jsonLen <= len(data) {
		jsonBytes := data[8 : 8+jsonLen]
		if looksLikeJSON(jsonBytes) {
			header := data[:4]
			parsed := prettyJSON(string(jsonBytes))
			chessMoves := extractChessMoves(string(jsonBytes))
			result := fmt.Sprintf("[二进制协议] header=%X len=%d\n%s", header, jsonLen, parsed)
			if chessMoves != "" {
				result = "🎯 行棋数据!\n" + chessMoves + "\n\n原始:\n" + result
			}
			return result
		}
	}

	// 尝试 [4字节大端长度 + JSON] 格式
	if len(data) >= 4 {
		jsonLen2 := int(binary.BigEndian.Uint32(data[0:4]))
		if jsonLen2 > 0 && 4+jsonLen2 <= len(data) {
			jsonBytes := data[4 : 4+jsonLen2]
			if looksLikeJSON(jsonBytes) {
				parsed := prettyJSON(string(jsonBytes))
				chessMoves := extractChessMoves(string(jsonBytes))
				result := fmt.Sprintf("[二进制协议] len=%d\n%s", jsonLen2, parsed)
				if chessMoves != "" {
					result = "🎯 行棋数据!\n" + chessMoves + "\n\n原始:\n" + result
				}
				return result
			}
		}
	}

	// 尝试整体是 JSON（有时二进制帧也直接包含 JSON）
	if looksLikeJSON(data) {
		parsed := prettyJSON(string(data))
		chessMoves := extractChessMoves(string(data))
		if chessMoves != "" {
			return "🎯 行棋数据!\n" + chessMoves + "\n\n原始:\n" + parsed
		}
		return parsed
	}

	// 无法解析，显示十六进制
	if len(data) <= 256 {
		return fmt.Sprintf("[binary %d bytes]\n%s", len(data), hexDump(data))
	}
	return fmt.Sprintf("[binary %d bytes]\n%s\n...", len(data), hexDump(data[:128]))
}

// looksLikeJSON 简单判断是否像 JSON
func looksLikeJSON(b []byte) bool {
	if len(b) == 0 {
		return false
	}
	first := b[0]
	return (first == '{' || first == '[') && utf8.Valid(b)
}

// prettyJSON 格式化 JSON 字符串
func prettyJSON(s string) string {
	var buf strings.Builder
	indent := 0
	inString := false
	esc := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if esc {
			buf.WriteByte(c)
			esc = false
			continue
		}
		if c == '\\' && inString {
			buf.WriteByte(c)
			esc = true
			continue
		}
		if c == '"' {
			inString = !inString
			buf.WriteByte(c)
			continue
		}
		if inString {
			buf.WriteByte(c)
			continue
		}
		switch c {
		case '{':
			buf.WriteByte(c)
			buf.WriteByte('\n')
			indent++
			buf.WriteString(strings.Repeat("  ", indent))
		case '}':
			buf.WriteByte('\n')
			indent--
			if indent < 0 {
				indent = 0
			}
			buf.WriteString(strings.Repeat("  ", indent))
			buf.WriteByte(c)
		case '[':
			buf.WriteByte(c)
			buf.WriteByte('\n')
			indent++
			buf.WriteString(strings.Repeat("  ", indent))
		case ']':
			buf.WriteByte('\n')
			indent--
			if indent < 0 {
				indent = 0
			}
			buf.WriteString(strings.Repeat("  ", indent))
			buf.WriteByte(c)
		case ',':
			buf.WriteByte(c)
			buf.WriteByte('\n')
			buf.WriteString(strings.Repeat("  ", indent))
		case ':':
			buf.WriteByte(c)
			buf.WriteByte(' ')
		default:
			buf.WriteByte(c)
		}
	}
	return buf.String()
}

// hexDump 生成 hexdump 格式
func hexDump(data []byte) string {
	var sb strings.Builder
	for i := 0; i < len(data); i += 16 {
		end := i + 16
		if end > len(data) {
			end = len(data)
		}
		fmt.Fprintf(&sb, "%04X  ", i)
		for j := i; j < end; j++ {
			fmt.Fprintf(&sb, "%02X ", data[j])
		}
		for j := end; j < i+16; j++ {
			sb.WriteString("   ")
		}
		sb.WriteString(" |")
		for j := i; j < end; j++ {
			c := data[j]
			if c >= 32 && c < 127 {
				sb.WriteByte(c)
			} else {
				sb.WriteByte('.')
			}
		}
		sb.WriteString("|\n")
	}
	return sb.String()
}

// extractChessMoves 从 JSON 中提取行棋信息，返回人类可读描述
func extractChessMoves(jsonStr string) string {
	lower := strings.ToLower(jsonStr)

	// 常见象棋行棋字段关键词
	chessMoveKeywords := []string{
		"move", "chess_move", "chessdata", "棋步", "movechess",
		"movepiece", "chessmove", "op_chess", "action_move",
		"src_pos", "dst_pos", "from_pos", "to_pos",
		"srcpos", "dstpos", "frompos", "topos",
		"step", "棋子", "走棋",
	}

	for _, kw := range chessMoveKeywords {
		if strings.Contains(lower, kw) {
			// 找到行棋相关字段，返回格式化后的 JSON 片段
			return fmt.Sprintf("[包含行棋字段: %q]\n%s", kw, prettyJSON(jsonStr))
		}
	}
	return ""
}

// wsRelayWithLog 单向转发 WebSocket 帧并记录日志
func (p *Proxy) wsRelayWithLog(src io.Reader, dst io.Writer, wsURL, host, direction string, done chan struct{}) {
	defer func() { done <- struct{}{} }()
	for {
		frame, err := readWSFrame(src)
		if err != nil {
			if !strings.Contains(err.Error(), "EOF") &&
				!strings.Contains(err.Error(), "use of closed") &&
				!strings.Contains(err.Error(), "connection reset") {
				log.Printf("[WS] %s read frame error: %v", direction, err)
			}
			return
		}

		// 只记录数据帧（TEXT/BINARY），控制帧（PING/PONG/CLOSE）不记录
		if frame.Opcode == wsOpcodeText || frame.Opcode == wsOpcodeBinary {
			body := payloadStr(frame)
			entry := &logger.Entry{
				Time:     time.Now(),
				Method:   "WS " + direction,
				URL:      wsURL,
				Host:     host,
				Protocol: "WSS",
				RespBody: body,
			}
			if direction == "C→S" {
				entry.ReqBody = body
				entry.RespBody = ""
			}
			p.logger.Add(entry)
			// 尝试解析行棋并打印终端
			printChessMoveIfMatch(body, direction, p.logger, p.advisor)
			// 对局生命周期事件识别
			if direction == "S→C" {
				detectGameEvents(body, p.advisor)
			} else {
				detectClientEvents(body, p.advisor)
			}
		}

		// 透传原始帧（保持掩码不变）
		if _, err := dst.Write(frame.Raw); err != nil {
			if !strings.Contains(err.Error(), "use of closed") {
				log.Printf("[WS] %s write frame error: %v", direction, err)
			}
			return
		}

		// 收到 CLOSE 帧，优雅退出
		if frame.Opcode == wsOpcodeClose {
			return
		}
	}
}
