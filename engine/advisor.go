package engine

import (
	"log"
	"sync"
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

// Advisor 持有引擎与棋盘，根据行棋数据自动推荐着法
type Advisor struct {
	mu         sync.Mutex
	engine     *Engine
	mySeat     int    // 我方席位 0=黑 1=红，-1=未识别
	redSeat    int    // 红方席位（来自 chesssetcolor_ack_msg），-1=未知
	movetimeMs int    // 引擎思考时长
	board      *Board // 当前棋盘（按 ack 顺序累积）
	curMatchID int64  // 当前对局 ID
	thinking   bool   // 引擎正在思考时跳过新请求

	// 上一次给出的推荐着法（UCI 格式），用于对比我方实际走法
	lastBest      string
	lastBestPiece byte // 推荐时起点棋子
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
	defer a.mu.Unlock()
	if a.mySeat == seatOrder {
		return
	}
	a.mySeat = seatOrder
	a.tryAnnounceColor()
}

// SetRedSeat 由 chesssetcolor_ack_msg 调用，告知红方席位编号
func (a *Advisor) SetRedSeat(redSeat int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.redSeat == redSeat {
		return
	}
	a.redSeat = redSeat
	a.tryAnnounceColor()
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

// OnGameStart 对局开始：始终重置棋盘和颜色，避免 matchID 复用的对局间状态串扰
func (a *Advisor) OnGameStart(matchID int64, matchName string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.board = InitStandard()
	a.curMatchID = matchID
	a.lastBest = ""
	a.lastBestPiece = 0
	a.mySeat = -1
	a.redSeat = -1
	log.Println("==================================================")
	log.Printf("[新局] %s [matchid=%d]", matchName, matchID)
	log.Println("==================================================")
}

// OnGameOver 服务器宣告对局结束（绝杀 / 超时判负 / 等）
func (a *Advisor) OnGameOver(winnerSeat int, text string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.curMatchID == 0 {
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
}

// OnGameLeave 客户端主动离开/退出/认输（leave_req_msg / exitmatch_req_msg）
func (a *Advisor) OnGameLeave(matchID int64, reason string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.curMatchID == 0 || a.curMatchID != matchID {
		return // 已结束 或 不匹配（避免在大厅菜单触发的离开误报）
	}
	log.Printf("[结束] matchid=%d  原因: %s（手动）", a.curMatchID, reason)
	log.Println("==================================================")
	a.lastBest = ""
	a.lastBestPiece = 0
	a.curMatchID = 0
}

// OnMove 接收一条已确认（ack）的行棋数据，更新棋盘并按需触发引擎推荐
func (a *Advisor) OnMove(m MoveInfo) {
	a.mu.Lock()

	// 兜底：如果 advisor 还不知道当前对局（可能进程启动晚了），自动开局
	if m.MatchID != a.curMatchID {
		a.board = InitStandard()
		a.curMatchID = m.MatchID
		a.lastBest = ""
		a.lastBestPiece = 0
		log.Printf("[advisor] 检测到对局 %d（兜底重置棋盘）", m.MatchID)
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

	// mySeat 未识别时不推荐
	if a.mySeat < 0 {
		a.mu.Unlock()
		return
	}

	// 是否轮到我方？
	myTurn := m.NextSeat == a.mySeat
	if !myTurn {
		a.mu.Unlock()
		return
	}

	// 思考中跳过
	if a.thinking {
		a.mu.Unlock()
		log.Printf("[advisor] 上一次思考未完成，跳过本次推荐")
		return
	}
	a.thinking = true
	fen := a.board.ToFEN()
	a.mu.Unlock()

	// 异步调引擎，不阻塞 WebSocket 转发
	go a.askEngine(fen)
}

func (a *Advisor) askEngine(fen string) {
	defer func() {
		a.mu.Lock()
		a.thinking = false
		a.mu.Unlock()
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

// Stop 停止内部引擎
func (a *Advisor) Stop() {
	if a.engine != nil {
		_ = a.engine.Stop()
	}
}
