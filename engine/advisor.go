package engine

import (
	"log"
	"sync"
	"time"
)

// MoveInfo 一次行棋数据（来自服务器 ack 包）
type MoveInfo struct {
	MatchID  int64
	Seat     int // 0=黑 1=红
	X1, Y1   int
	X2, Y2   int
	NextSeat int
	MoveType int
}

// HistoryEntry 一条棋步历史记录
type HistoryEntry struct {
	Index   int    `json:"index"`
	Seat    int    `json:"seat"`
	Color   string `json:"color"` // "红方" / "黑方"
	Piece   string `json:"piece"` // 中文名
	UCI     string `json:"uci"`
	FromX   int    `json:"fromX"`
	FromY   int    `json:"fromY"`
	ToX     int    `json:"toX"`
	ToY     int    `json:"toY"`
	Capture bool   `json:"capture"`
	Time    int64  `json:"time"`
}

// MoveMark 棋盘上需高亮的一步
type MoveMark struct {
	FromX int    `json:"fromX"`
	FromY int    `json:"fromY"`
	ToX   int    `json:"toX"`
	ToY   int    `json:"toY"`
	Piece string `json:"piece"`
	UCI   string `json:"uci"`
}

// Snapshot 供前端渲染的整局快照
type Snapshot struct {
	Active    bool           `json:"active"`
	MatchID   int64          `json:"matchID"`
	MatchName string         `json:"matchName"`
	Cells     [10][9]string  `json:"cells"`   // FEN 单字符（"R"/"r"/""等）
	MyColor   int            `json:"myColor"` // 1=红 0=黑 -1=未识别
	MySeat    int            `json:"mySeat"`
	RedSeat   int            `json:"redSeat"`
	CurSide   int            `json:"curSide"` // 1=红 0=黑
	LastMove  *MoveMark      `json:"lastMove,omitempty"`
	Recommend *MoveMark      `json:"recommend,omitempty"`
	History   []HistoryEntry `json:"history"`
	Thinking  bool           `json:"thinking"`
	FEN       string         `json:"fen"`
	Updated   int64          `json:"updated"`
}

// Advisor 持有引擎与棋盘，根据行棋数据自动推荐着法
type Advisor struct {
	mu         sync.Mutex
	engine     *Engine
	mySeat     int    // 我方席位 0=黑 1=红，-1=未识别
	redSeat    int    // 红方席位（来自 chesssetcolor_ack_msg），-1=未知
	movetimeMs int    // 引擎思考时长
	board      *Board // 当前棋盘（按 ack 顺序累积）
	curMatchID int64  // 当前对局 ID
	matchName  string // 当前对局名称
	thinking   bool   // 引擎正在思考时跳过新请求

	// 上一次给出的推荐着法（UCI 格式），用于对比我方实际走法
	lastBest      string
	lastBestPiece byte // 推荐时起点棋子

	// Web/可视化需要的开拓信息
	history  []HistoryEntry
	lastMove *MoveMark
	watcher  func() // 状态变更后回调（在锁外异步调用）
}

// NewAdvisor 创建一个推荐器
//
//	mySeat: 0=黑 1=红，-1=自动识别
//	movetimeMs: 每步思考时长（毫秒）
func NewAdvisor(eng *Engine, mySeat, movetimeMs int) *Advisor {
	return &Advisor{
		engine:     eng,
		mySeat:     mySeat,
		redSeat:    -1,
		movetimeMs: movetimeMs,
		board:      InitStandard(),
	}
}

// GetMySeat 返回我方席位编号（0 或 1，-1=未识别，不是颜色！）
func (a *Advisor) GetMySeat() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.mySeat
}

// MyColor 返回我方颜色（1=红方 0=黑方 -1=未识别）
func (a *Advisor) MyColor() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.myColorLocked()
}

// myColorLocked 需持有 a.mu 锁调用
func (a *Advisor) myColorLocked() int {
	if a.mySeat < 0 || a.redSeat < 0 {
		return -1
	}
	if a.mySeat == a.redSeat {
		return 1 // 我方席位就是红方席位
	}
	return 0
}

// SeatColorName 根据 redSeat 动态判断某席位是红是黑
func (a *Advisor) SeatColorName(seat int) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.redSeat >= 0 && seat >= 0 {
		if seat == a.redSeat {
			return "红方"
		}
		return "黑方"
	}
	// 未知时回退到以前的写死逻辑
	if seat == 1 {
		return "红方"
	}
	return "黑方"
}

// SetMySeatOrder 由 enterround_ack_msg 调用，告知我方席位编号
func (a *Advisor) SetMySeatOrder(seatOrder int) {
	a.mu.Lock()
	if a.mySeat == seatOrder {
		a.mu.Unlock()
		return
	}
	a.mySeat = seatOrder
	a.tryAnnounceColor()
	a.mu.Unlock()
	a.notify()
}

// SetRedSeat 由 chesssetcolor_ack_msg 调用，告知红方席位编号
func (a *Advisor) SetRedSeat(redSeat int) {
	a.mu.Lock()
	if a.redSeat == redSeat {
		a.mu.Unlock()
		return
	}
	a.redSeat = redSeat
	a.tryAnnounceColor()
	a.mu.Unlock()
	a.notify()
}

// tryAnnounceColor 当 mySeat 和 redSeat 都已知时，推断并打印颜色
func (a *Advisor) tryAnnounceColor() {
	if a.mySeat < 0 || a.redSeat < 0 {
		return
	}
	color := "黑方"
	if a.mySeat == a.redSeat {
		color = "红方"
	}
	log.Printf("[颜色] 我方席位=%d, 红方席位=%d => 我执%s", a.mySeat, a.redSeat, color)
}

// resetBoardLocked 重置当前对局的棋盘、历史、高亮、推荐状态。需持锁调用。
// matchID/mySeat/redSeat 请调用者自行赋值（不同路径语义不同）。
func (a *Advisor) resetBoardLocked() {
	a.board = InitStandard()
	a.history = nil
	a.lastMove = nil
	a.lastBest = ""
	a.lastBestPiece = 0
}

// OnGameStart 对局开始：始终重置棋盘和颜色，避免 matchID 复用的对局间状态串扰
func (a *Advisor) OnGameStart(matchID int64, matchName string) {
	a.mu.Lock()
	a.resetBoardLocked()
	a.curMatchID = matchID
	a.matchName = matchName
	a.mySeat = -1
	a.redSeat = -1
	a.mu.Unlock()
	log.Println("==================================================")
	log.Printf("[新局] %s [matchid=%d]", matchName, matchID)
	log.Println("==================================================")
	a.notify()
}

// OnGameOver 服务器宣告对局结束（绝杀 / 超时判负 / 等）
func (a *Advisor) OnGameOver(winnerSeat int, text string) {
	a.mu.Lock()
	if a.curMatchID == 0 {
		a.mu.Unlock()
		return // 已经被处理过
	}
	winnerName := SeatName(winnerSeat)
	result := "未知"
	if a.mySeat >= 0 {
		if winnerSeat == a.mySeat {
			result = "我方胜"
		} else {
			result = "对方胜"
		}
	}
	log.Printf("[结束] matchid=%d  胜方: %s  结果: %s  原因: %s",
		a.curMatchID, winnerName, result, text)
	log.Println("==================================================")
	a.lastBest = ""
	a.lastBestPiece = 0
	a.curMatchID = 0
	a.lastMove = nil
	a.mu.Unlock()
	a.notify()
}

// OnGameLeave 客户端主动离开/退出/认负（leave_req_msg / exitmatch_req_msg）
func (a *Advisor) OnGameLeave(matchID int64, reason string) {
	a.mu.Lock()
	if a.curMatchID == 0 || a.curMatchID != matchID {
		a.mu.Unlock()
		return // 已结束 或 不匹配（避免在大厅菜单触发的离开误报）
	}
	log.Printf("[结束] matchid=%d  原因: %s（手动）", a.curMatchID, reason)
	log.Println("==================================================")
	a.lastBest = ""
	a.lastBestPiece = 0
	a.curMatchID = 0
	a.lastMove = nil
	a.mu.Unlock()
	a.notify()
}

// OnMove 接收一条已确认（ack）的行棋数据，更新棋盘并按需触发引擎推荐
func (a *Advisor) OnMove(m MoveInfo) {
	a.mu.Lock()

	// 兜底：如果 advisor 还不知道当前对局（进程启动晚，或上局结束后未收到 entermatch_ack_msg）
	// 必须连同 history/lastMove 一起重置，否则新局棋盘会残留上一局的历史/高亮
	if m.MatchID != a.curMatchID {
		a.resetBoardLocked()
		a.curMatchID = m.MatchID
		a.matchName = ""
		log.Printf("[advisor] 检测到新对局 %d（兜底重置棋盘）", m.MatchID)
	}

	// 应用棋步前先取起点棋子（应用后会被清空）
	piece := a.board.PieceAt(m.X1, m.Y1)
	actualUCI := XYToUCI(m.X1, m.Y1, m.X2, m.Y2)
	myColor := a.myColorLocked()
	dx1, dy1 := DisplayXY(m.X1, m.Y1, myColor)
	dx2, dy2 := DisplayXY(m.X2, m.Y2, myColor)
	isMine := a.mySeat >= 0 && m.Seat == a.mySeat

	// 我方走子：与上次推荐对比
	if isMine && a.lastBest != "" {
		if a.lastBest == actualUCI {
			log.Printf("[一致] 你走的 %s（%s）与引擎推荐完全一致", actualUCI, PieceName(piece))
		} else {
			bx1, by1, bx2, by2, _ := UCIToXY(a.lastBest)
			bdx1, bdy1 := DisplayXY(bx1, by1, myColor)
			bdx2, bdy2 := DisplayXY(bx2, by2, myColor)
			log.Printf("[偏离] 你走的 %s（%s  %d,%d->%d,%d），引擎推荐 %s（%s  %d,%d->%d,%d）",
				actualUCI, PieceName(piece), dx1, dy1, dx2, dy2,
				a.lastBest, PieceName(a.lastBestPiece), bdx1, bdy1, bdx2, bdy2)
		}
		a.lastBest = ""
		a.lastBestPiece = 0
	}

	// 应用棋步
	a.board.ApplyMove(m.X1, m.Y1, m.X2, m.Y2)

	// 记录历史与高亮
	a.lastMove = &MoveMark{
		FromX: m.X1, FromY: m.Y1,
		ToX: m.X2, ToY: m.Y2,
		Piece: PieceName(piece),
		UCI:   actualUCI,
	}
	a.history = append(a.history, HistoryEntry{
		Index: len(a.history) + 1,
		Seat:  m.Seat,
		Color: a.seatColorNameLocked(m.Seat),
		Piece: PieceName(piece),
		UCI:   actualUCI,
		FromX: m.X1, FromY: m.Y1,
		ToX: m.X2, ToY: m.Y2,
		Capture: m.MoveType == 1,
		Time:    time.Now().UnixMilli(),
	})

	// mySeat 未识别时不推荐
	if a.mySeat < 0 {
		a.mu.Unlock()
		a.notify()
		return
	}

	// 是否轮到我方？
	myTurn := m.NextSeat == a.mySeat
	if !myTurn {
		a.mu.Unlock()
		a.notify()
		return
	}

	// 思考中跳过
	if a.thinking {
		a.mu.Unlock()
		log.Printf("[advisor] 上一次思考未完成，跳过本次推荐")
		a.notify()
		return
	}
	a.thinking = true
	fen := a.board.ToFEN()
	a.mu.Unlock()
	a.notify()

	// 异步调引擎，不阻塞 WebSocket 转发
	go a.askEngine(fen)
}

func (a *Advisor) askEngine(fen string) {
	defer func() {
		a.mu.Lock()
		a.thinking = false
		a.mu.Unlock()
		a.notify()
	}()

	best, err := a.engine.BestMove(fen, a.movetimeMs)
	if err != nil {
		log.Printf("[advisor] 引擎错误: %v", err)
		return
	}
	x1, y1, x2, y2, ok := UCIToXY(best)
	if !ok {
		log.Printf("[advisor] 推荐着法解析失败: %s", best)
		return
	}

	a.mu.Lock()
	piece := a.board.PieceAt(x1, y1)
	target := a.board.PieceAt(x2, y2)
	a.lastBest = best
	a.lastBestPiece = piece
	myColor := a.myColorLocked()
	a.mu.Unlock()

	dx1, dy1 := DisplayXY(x1, y1, myColor)
	dx2, dy2 := DisplayXY(x2, y2, myColor)
	if target == 0 {
		log.Printf("[推荐] %s %s (%d,%d) -> (%d,%d)", PieceName(piece), best, dx1, dy1, dx2, dy2)
	} else {
		log.Printf("[推荐] %s %s (%d,%d) 吃 %s (%d,%d)",
			PieceName(piece), best, dx1, dy1, PieceName(target), dx2, dy2)
	}
}

// SetWatcher 注入一个状态变更回调（例如 webui broadcast）
func (a *Advisor) SetWatcher(fn func()) {
	a.mu.Lock()
	a.watcher = fn
	a.mu.Unlock()
}

// notify 在锁外异步调用 watcher，避免重入锁死锁
func (a *Advisor) notify() {
	a.mu.Lock()
	w := a.watcher
	a.mu.Unlock()
	if w != nil {
		go w()
	}
}

// seatColorNameLocked 需持锁调用
func (a *Advisor) seatColorNameLocked(seat int) string {
	if a.redSeat >= 0 && seat >= 0 {
		if seat == a.redSeat {
			return "红方"
		}
		return "黑方"
	}
	if seat == 1 {
		return "红方"
	}
	return "黑方"
}

// Snapshot 返回当前状态的完整快照
func (a *Advisor) Snapshot() Snapshot {
	a.mu.Lock()
	defer a.mu.Unlock()

	var cells [10][9]string
	if a.board != nil {
		for y := 0; y < 10; y++ {
			for x := 0; x < 9; x++ {
				c := a.board.Cells[y][x]
				if c == 0 {
					cells[y][x] = ""
				} else {
					cells[y][x] = string(c)
				}
			}
		}
	}

	curSide := -1
	fen := ""
	if a.board != nil {
		if a.board.SideRed {
			curSide = 1
		} else {
			curSide = 0
		}
		fen = a.board.ToFEN()
	}

	var rec *MoveMark
	if a.lastBest != "" && a.curMatchID != 0 {
		x1, y1, x2, y2, ok := UCIToXY(a.lastBest)
		if ok {
			rec = &MoveMark{
				FromX: x1, FromY: y1,
				ToX: x2, ToY: y2,
				Piece: PieceName(a.lastBestPiece),
				UCI:   a.lastBest,
			}
		}
	}

	var lastMoveCopy *MoveMark
	if a.lastMove != nil {
		lm := *a.lastMove
		lastMoveCopy = &lm
	}

	hist := make([]HistoryEntry, len(a.history))
	copy(hist, a.history)

	return Snapshot{
		Active:    a.curMatchID != 0,
		MatchID:   a.curMatchID,
		MatchName: a.matchName,
		Cells:     cells,
		MyColor:   a.myColorLocked(),
		MySeat:    a.mySeat,
		RedSeat:   a.redSeat,
		CurSide:   curSide,
		LastMove:  lastMoveCopy,
		Recommend: rec,
		History:   hist,
		Thinking:  a.thinking,
		FEN:       fen,
		Updated:   time.Now().UnixMilli(),
	}
}

// Stop 停止内部引擎
func (a *Advisor) Stop() {
	if a.engine != nil {
		_ = a.engine.Stop()
	}
}
