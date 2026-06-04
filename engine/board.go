package engine

import (
	"strconv"
	"strings"
)

// FEN 棋子字符（红大写 / 黑小写）：
//   K/k  将/帅
//   A/a  士/仕
//   B/b  象/相
//   N/n  马
//   R/r  车
//   C/c  炮
//   P/p  卒/兵

// Board 表示一个中国象棋棋盘
// Cells[y][x]：y 是行（红方底=0，黑方底=9），x 是列（a=0..i=8）
type Board struct {
	Cells   [10][9]byte // 0 表示空格
	SideRed bool        // true 表示当前轮到红方走子
	Move    int         // 全步数（每完成一回合 +1）
}

// InitStandard 返回开局初始局面
func InitStandard() *Board {
	b := &Board{SideRed: true, Move: 1}

	// 红方底线 y=0
	red0 := [9]byte{'R', 'N', 'B', 'A', 'K', 'A', 'B', 'N', 'R'}
	// 黑方底线 y=9
	black9 := [9]byte{'r', 'n', 'b', 'a', 'k', 'a', 'b', 'n', 'r'}
	b.Cells[0] = red0
	b.Cells[9] = black9

	// 红炮 y=2: x=1, x=7
	b.Cells[2][1] = 'C'
	b.Cells[2][7] = 'C'
	// 黑炮 y=7
	b.Cells[7][1] = 'c'
	b.Cells[7][7] = 'c'

	// 红兵 y=3: x=0,2,4,6,8
	for _, x := range []int{0, 2, 4, 6, 8} {
		b.Cells[3][x] = 'P'
	}
	// 黑卒 y=6
	for _, x := range []int{0, 2, 4, 6, 8} {
		b.Cells[6][x] = 'p'
	}
	return b
}

// ApplyMove 将一步棋应用到棋盘上（无校验）
// 调用方应确保起点确实有棋子
func (b *Board) ApplyMove(x1, y1, x2, y2 int) {
	if !inBoard(x1, y1) || !inBoard(x2, y2) {
		return
	}
	piece := b.Cells[y1][x1]
	b.Cells[y1][x1] = 0
	b.Cells[y2][x2] = piece

	// 切换走子方
	if b.SideRed {
		b.SideRed = false
	} else {
		b.SideRed = true
		b.Move++
	}
}

// PieceAt 返回 (x,y) 处的棋子，0 表示空
func (b *Board) PieceAt(x, y int) byte {
	if !inBoard(x, y) {
		return 0
	}
	return b.Cells[y][x]
}

// String 输出可视化棋盘（行 9→0），便于调试
func (b *Board) String() string {
	var sb strings.Builder
	for y := 9; y >= 0; y-- {
		sb.WriteString(strconv.Itoa(y))
		sb.WriteByte(' ')
		for x := 0; x < 9; x++ {
			c := b.Cells[y][x]
			if c == 0 {
				sb.WriteString(". ")
			} else {
				sb.WriteByte(c)
				sb.WriteByte(' ')
			}
		}
		sb.WriteByte('\n')
	}
	sb.WriteString("  a b c d e f g h i\n")
	return sb.String()
}

func inBoard(x, y int) bool {
	return x >= 0 && x < 9 && y >= 0 && y < 10
}
