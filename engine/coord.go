package engine

import "fmt"

// 协议 (x,y) 坐标系（已验证）：
//   x: 列 0..8 → UCI 字母 a..i（红方左到右）
//   y: 行 0..9（红方底=0，黑方底=9）
// 与 UCI/FEN 标准坐标系完全一致。

// XYToUCI 将协议坐标 (x,y) 转 UCI 走法字符串，例如 (7,2)→(4,2) = "h2e2"
func XYToUCI(x1, y1, x2, y2 int) string {
	return fmt.Sprintf("%c%d%c%d", 'a'+x1, y1, 'a'+x2, y2)
}

// UCIToXY 反向转换 UCI 走法字符串到协议坐标
func UCIToXY(uci string) (x1, y1, x2, y2 int, ok bool) {
	if len(uci) != 4 {
		return 0, 0, 0, 0, false
	}
	x1 = int(uci[0] - 'a')
	y1 = int(uci[1] - '0')
	x2 = int(uci[2] - 'a')
	y2 = int(uci[3] - '0')
	if x1 < 0 || x1 > 8 || x2 < 0 || x2 > 8 || y1 < 0 || y1 > 9 || y2 < 0 || y2 > 9 {
		return 0, 0, 0, 0, false
	}
	return x1, y1, x2, y2, true
}

// SeatName 0=黑方 1=红方
func SeatName(seat int) string {
	if seat == 1 {
		return "红方"
	}
	return "黑方"
}

// DisplayXY 将内部 0-based 坐标转换为用户观看的 1-based 坐标
//
// myColor: 1=我执红方 0=我执黑方 -1=未识别（默认红方视角）
// 始终保证“我方右下角 = (1,1)”，X 从右到左=1..9，Y 从下到上=1..10
func DisplayXY(x, y, myColor int) (int, int) {
	if myColor == 0 {
		// 黑方视角：黑方右=内部x=0，黑方下=内部y=9
		return x + 1, 10 - y
	}
	// 红方视角（默认）：红方右=内部x=8，红方下=内部y=0
	return 9 - x, y + 1
}

// PieceName 返回棋子的中文名称
func PieceName(p byte) string {
	switch p {
	case 'K':
		return "红帅"
	case 'A':
		return "红仕"
	case 'B':
		return "红相"
	case 'N':
		return "红马"
	case 'R':
		return "红车"
	case 'C':
		return "红炮"
	case 'P':
		return "红兵"
	case 'k':
		return "黑将"
	case 'a':
		return "黑士"
	case 'b':
		return "黑象"
	case 'n':
		return "黑马"
	case 'r':
		return "黑车"
	case 'c':
		return "黑炮"
	case 'p':
		return "黑卒"
	case 0:
		return "空位"
	}
	return "?"
}
