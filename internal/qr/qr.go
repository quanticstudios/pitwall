// Package qr encodes text as a QR code: byte mode, error correction level
// M, versions 1 to 10, which hold up to 213 bytes. That fits a pairing URL;
// longer text is an error.
//
// The steps follow ISO/IEC 18004 as Project Nayuki's QR Code generator
// (MIT) lays them out; see THIRD_PARTY_NOTICES.md.
package qr

import (
	"errors"
	"strings"
)

// block is one version's level-M error correction layout: ec codewords per
// block, then the count and data length of each group of blocks.
type block struct{ ec, n1, d1, n2, d2 int }

var blocks = [11]block{
	1: {10, 1, 16, 0, 0}, 2: {16, 1, 28, 0, 0}, 3: {26, 1, 44, 0, 0}, 4: {18, 2, 32, 0, 0},
	5: {24, 2, 43, 0, 0}, 6: {16, 4, 27, 0, 0}, 7: {18, 4, 31, 0, 0}, 8: {22, 2, 38, 2, 39},
	9: {22, 3, 36, 2, 37}, 10: {26, 4, 43, 1, 44},
}

// align are the alignment pattern centers per version.
var align = [11][]int{
	2: {6, 18}, 3: {6, 22}, 4: {6, 26}, 5: {6, 30}, 6: {6, 34},
	7: {6, 22, 38}, 8: {6, 24, 42}, 9: {6, 26, 46}, 10: {6, 28, 50},
}

// ErrTooLong is returned for text over 213 bytes.
var ErrTooLong = errors.New("qr: text too long")

// Code is a QR code's modules, row by row; true is dark. It has no quiet
// zone: leave four modules of light around it.
type Code [][]bool

// Encode returns the smallest code that holds text.
func Encode(text string) (Code, error) {
	ver := 0
	for v := 1; v <= 10; v++ {
		b := blocks[v]
		count := 8
		if v >= 10 {
			count = 16
		}
		if 4+count+8*len(text) <= 8*(b.n1*b.d1+b.n2*b.d2) {
			ver = v
			break
		}
	}
	if ver == 0 {
		return nil, ErrTooLong
	}
	q := newSymbol(ver)
	q.place(codewords(ver, text))
	best, bestScore := Code(nil), -1
	for mask := range 8 {
		c := q.finish(mask)
		if s := penalty(c); bestScore < 0 || s < bestScore {
			best, bestScore = c, s
		}
	}
	return best, nil
}

// codewords are text's data codewords with their error correction,
// interleaved in the order they are placed.
func codewords(ver int, text string) []byte {
	b := blocks[ver]
	capacity := b.n1*b.d1 + b.n2*b.d2
	var bits []bool
	put := func(v, n int) {
		for i := n - 1; i >= 0; i-- {
			bits = append(bits, v>>i&1 == 1)
		}
	}
	put(4, 4) // byte mode
	if ver >= 10 {
		put(len(text), 16)
	} else {
		put(len(text), 8)
	}
	for i := 0; i < len(text); i++ {
		put(int(text[i]), 8)
	}
	put(0, min(4, 8*capacity-len(bits)))
	put(0, (8-len(bits)%8)%8)
	data := make([]byte, 0, capacity)
	for i := 0; i < len(bits); i += 8 {
		var v byte
		for _, bit := range bits[i : i+8] {
			v <<= 1
			if bit {
				v |= 1
			}
		}
		data = append(data, v)
	}
	for pad := byte(0xEC); len(data) < capacity; pad ^= 0xEC ^ 0x11 {
		data = append(data, pad)
	}

	div := rsDivisor(b.ec)
	var ds, es [][]byte
	for i := range b.n1 + b.n2 {
		n := b.d1
		if i >= b.n1 {
			n = b.d2
		}
		ds = append(ds, data[:n])
		es = append(es, rsRemainder(data[:n], div))
		data = data[n:]
	}
	var out []byte
	for i := range max(b.d1, b.d2) {
		for _, d := range ds {
			if i < len(d) {
				out = append(out, d[i])
			}
		}
	}
	for i := range b.ec {
		for _, e := range es {
			out = append(out, e[i])
		}
	}
	return out
}

// Adapted from Project Nayuki's QR Code generator (MIT): qrcodegen.go,
// reedSolomonMultiply, reedSolomonComputeDivisor and
// reedSolomonComputeRemainder.

// gfMul multiplies in GF(2^8) modulo x^8+x^4+x^3+x^2+1.
func gfMul(x, y byte) byte {
	var z int
	for i := 7; i >= 0; i-- {
		z = z<<1 ^ (z>>7)*0x11D
		z ^= int(y>>i&1) * int(x)
	}
	return byte(z)
}

// rsDivisor is the Reed-Solomon generator polynomial of degree deg, highest
// term first, its leading 1 left out.
func rsDivisor(deg int) []byte {
	r := make([]byte, deg)
	r[deg-1] = 1
	root := byte(1)
	for range deg {
		for j := range r {
			r[j] = gfMul(r[j], root)
			if j+1 < len(r) {
				r[j] ^= r[j+1]
			}
		}
		root = gfMul(root, 2)
	}
	return r
}

// rsRemainder is data's error correction codewords.
func rsRemainder(data, div []byte) []byte {
	r := make([]byte, len(div))
	for _, b := range data {
		f := b ^ r[0]
		copy(r, r[1:])
		r[len(r)-1] = 0
		for i := range r {
			r[i] ^= gfMul(div[i], f)
		}
	}
	return r
}

// symbol is a code being built: the modules and which of them are function
// patterns, which masks leave alone.
type symbol struct {
	ver      int
	dark, fn [][]bool
}

func newSymbol(ver int) *symbol {
	n := 17 + 4*ver
	q := &symbol{ver: ver}
	for range n {
		q.dark = append(q.dark, make([]bool, n))
		q.fn = append(q.fn, make([]bool, n))
	}
	for i := range n {
		q.set(6, i, i%2 == 0)
		q.set(i, 6, i%2 == 0)
	}
	for _, c := range [][2]int{{3, 3}, {n - 4, 3}, {3, n - 4}} {
		for dy := -4; dy <= 4; dy++ {
			for dx := -4; dx <= 4; dx++ {
				x, y := c[0]+dx, c[1]+dy
				if x >= 0 && x < n && y >= 0 && y < n {
					d := max(abs(dx), abs(dy))
					q.set(x, y, d != 2 && d != 4)
				}
			}
		}
	}
	a := align[ver]
	for i, ax := range a {
		for j, ay := range a {
			if i == 0 && j == 0 || i == 0 && j == len(a)-1 || i == len(a)-1 && j == 0 {
				continue // the finders
			}
			for dy := -2; dy <= 2; dy++ {
				for dx := -2; dx <= 2; dx++ {
					q.set(ax+dx, ay+dy, max(abs(dx), abs(dy)) != 1)
				}
			}
		}
	}
	q.format(0) // reserves the format modules
	if ver >= 7 {
		rem := ver
		for range 12 {
			rem = rem<<1 ^ (rem>>11)*0x1F25
		}
		bits := ver<<12 | rem
		for i := range 18 {
			a, b := n-11+i%3, i/3
			q.set(a, b, bits>>i&1 == 1)
			q.set(b, a, bits>>i&1 == 1)
		}
	}
	return q
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// set makes the module at column x, row y a function module.
func (q *symbol) set(x, y int, dark bool) {
	q.dark[y][x] = dark
	q.fn[y][x] = true
}

// format draws both copies of the format information for level M and mask.
func (q *symbol) format(mask int) {
	n := len(q.dark)
	data := mask // level M is 00
	rem := data
	for range 10 {
		rem = rem<<1 ^ (rem>>9)*0x537
	}
	bits := (data<<10 | rem) ^ 0x5412
	bit := func(i int) bool { return bits>>i&1 == 1 }
	for i := range 6 {
		q.set(8, i, bit(i))
	}
	q.set(8, 7, bit(6))
	q.set(8, 8, bit(7))
	q.set(7, 8, bit(8))
	for i := 9; i < 15; i++ {
		q.set(14-i, 8, bit(i))
	}
	for i := range 8 {
		q.set(n-1-i, 8, bit(i))
	}
	for i := 8; i < 15; i++ {
		q.set(8, n-15+i, bit(i))
	}
	q.set(8, n-8, true)
}

// place lays data out in the zigzag order, two columns at a time from the
// right, skipping function modules. Modules left over stay light.
func (q *symbol) place(data []byte) {
	n := len(q.dark)
	i := 0
	for right := n - 1; right >= 1; right -= 2 {
		if right == 6 {
			right = 5 // the vertical timing pattern
		}
		for vert := range n {
			for j := range 2 {
				x := right - j
				y := vert
				if (right+1)&2 == 0 {
					y = n - 1 - vert
				}
				if !q.fn[y][x] && i < len(data)*8 {
					q.dark[y][x] = data[i>>3]>>(7-i&7)&1 == 1
					i++
				}
			}
		}
	}
}

// finish returns a copy with mask applied to the data modules and the
// format information that names it.
func (q *symbol) finish(mask int) Code {
	c := &symbol{ver: q.ver}
	for y := range q.dark {
		c.dark = append(c.dark, append([]bool(nil), q.dark[y]...))
		c.fn = append(c.fn, append([]bool(nil), q.fn[y]...))
	}
	for y := range c.dark {
		for x := range c.dark[y] {
			if c.fn[y][x] {
				continue
			}
			var flip bool
			switch mask {
			case 0:
				flip = (x+y)%2 == 0
			case 1:
				flip = y%2 == 0
			case 2:
				flip = x%3 == 0
			case 3:
				flip = (x+y)%3 == 0
			case 4:
				flip = (x/3+y/2)%2 == 0
			case 5:
				flip = x*y%2+x*y%3 == 0
			case 6:
				flip = (x*y%2+x*y%3)%2 == 0
			case 7:
				flip = ((x+y)%2+x*y%3)%2 == 0
			}
			c.dark[y][x] = c.dark[y][x] != flip
		}
	}
	c.format(mask)
	return c.dark
}

// penalty scores how hard a masked code is to read; Encode keeps the mask
// with the lowest.
func penalty(c Code) int {
	n := len(c)
	at := func(x, y int, rows bool) bool {
		if rows {
			return c[y][x]
		}
		return c[x][y]
	}
	score := 0
	for _, rows := range []bool{true, false} {
		for y := range n {
			run := 0
			for x := range n {
				if x > 0 && at(x, y, rows) == at(x-1, y, rows) {
					run++
				} else {
					run = 1
				}
				switch {
				case run == 5:
					score += 3
				case run > 5:
					score++
				}
				if x >= 10 {
					var s strings.Builder
					for k := x - 10; k <= x; k++ {
						if at(k, y, rows) {
							s.WriteByte('1')
						} else {
							s.WriteByte('0')
						}
					}
					if p := s.String(); p == "10111010000" || p == "00001011101" {
						score += 40
					}
				}
			}
		}
	}
	darkCount := 0
	for y := range n {
		for x := range n {
			if c[y][x] {
				darkCount++
			}
			if x > 0 && y > 0 && c[y][x] == c[y-1][x] && c[y][x] == c[y][x-1] && c[y][x] == c[y-1][x-1] {
				score += 3
			}
		}
	}
	k := (abs(darkCount*20-n*n*10) + n*n - 1) / (n * n)
	return score + max(k-1, 0)*10
}

// String draws the code with Unicode half blocks, two rows to a line, dark
// modules as spaces on a light quiet zone, for a terminal with a dark
// background to show as a scannable code.
func (c Code) String() string {
	const quiet = 2
	n := len(c)
	light := func(x, y int) bool {
		return x < 0 || y < 0 || x >= n || y >= n || !c[y][x]
	}
	var b strings.Builder
	for y := -quiet; y < n+quiet; y += 2 {
		for x := -quiet; x < n+quiet; x++ {
			switch top, bot := light(x, y), light(x, y+1); {
			case top && bot:
				b.WriteString("█")
			case top:
				b.WriteString("▀")
			case bot:
				b.WriteString("▄")
			default:
				b.WriteByte(' ')
			}
		}
		b.WriteByte('\n')
	}
	return b.String()
}
