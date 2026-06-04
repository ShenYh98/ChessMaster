package engine

import (
	"strconv"
	"strings"
)

// ToFEN 将棋盘转为 FEN 字符串
// FEN 行序：从黑方底线 y=9 开始，到红方底线 y=0 结束，由 / 分隔
// 例：开局 "rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR w - - 0 1"
func (b *Board) ToFEN() string {
	var sb strings.Builder
	for y := 9; y >= 0; y-- {
		if y < 9 {
			sb.WriteByte('/')
		}
		empty := 0
		for x := 0; x < 9; x++ {
			c := b.Cells[y][x]
			if c == 0 {
				empty++
				continue
			}
			if empty > 0 {
				sb.WriteString(strconv.Itoa(empty))
				empty = 0
			}
			sb.WriteByte(c)
		}
		if empty > 0 {
			sb.WriteString(strconv.Itoa(empty))
		}
	}
	sb.WriteByte(' ')
	if b.SideRed {
		sb.WriteByte('w')
	} else {
		sb.WriteByte('b')
	}
	sb.WriteString(" - - 0 ")
	sb.WriteString(strconv.Itoa(b.Move))
	return sb.String()
}
