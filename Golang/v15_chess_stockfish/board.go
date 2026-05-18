package main

// ============================================================================
// BOARD — v6
// Piece, board representation, move generation, threat metrics
// ============================================================================

type Piece struct {
	Color string // "w" or "b"
	Type  string // K Q R B N P
}

type Board [8][8]*Piece

type Move struct {
	From [2]int
	To   [2]int
}

var pieceValue = map[string]float64{"K": 10, "Q": 8, "R": 5, "B": 3, "N": 3, "P": 1}
var pieceThreat = map[string]float64{"K": 0, "Q": 1.0, "R": 0.8, "B": 0.6, "N": 0.6, "P": 0.3}

func initialBoard() Board {
	var b Board
	back := []string{"R", "N", "B", "Q", "K", "B", "N", "R"}
	for f := 0; f < 8; f++ {
		b[0][f] = &Piece{"b", back[f]}
		b[1][f] = &Piece{"b", "P"}
		b[6][f] = &Piece{"w", "P"}
		b[7][f] = &Piece{"w", back[f]}
	}
	return b
}

func applyMoveInPlace(b *Board, m Move) (capturedPiece *Piece, promoted bool) {
    capturedPiece = b[m.To[0]][m.To[1]]
    b[m.To[0]][m.To[1]] = b[m.From[0]][m.From[1]]
    b[m.From[0]][m.From[1]] = nil
    p := b[m.To[0]][m.To[1]]
    if p != nil && p.Type == "P" && (m.To[0] == 0 || m.To[0] == 7) {
        b[m.To[0]][m.To[1]] = &Piece{p.Color, "Q"}
        promoted = true
    }
    return
}

func undoMoveInPlace(b *Board, m Move, capturedPiece *Piece, promoted bool) {
    p := b[m.To[0]][m.To[1]]
    if promoted {
        p = &Piece{p.Color, "P"}
    }
    b[m.From[0]][m.From[1]] = p
    b[m.To[0]][m.To[1]] = capturedPiece
}

func cloneBoard(b Board) Board {
	var nb Board
	for r := 0; r < 8; r++ {
		for f := 0; f < 8; f++ {
			if b[r][f] != nil {
				p := *b[r][f]
				nb[r][f] = &p
			}
		}
	}
	return nb
}

func inBounds(r, f int) bool { return r >= 0 && r < 8 && f >= 0 && f < 8 }

func opp(color string) string {
	if color == "w" {
		return "b"
	}
	return "w"
}

func getKingPos(b Board, color string) (int, int, bool) {
	for r := 0; r < 8; r++ {
		for f := 0; f < 8; f++ {
			if b[r][f] != nil && b[r][f].Color == color && b[r][f].Type == "K" {
				return r, f, true
			}
		}
	}
	return 0, 0, false
}

func isSquareAttacked(b Board, r, f int, byColor string) bool {
	// To find a pawn of byColor that attacks (r,f), look in the direction
	// OPPOSITE to that pawn's movement direction — i.e. "behind" (r,f)
	// relative to byColor's advance.
	// White pawns move toward row 0 (dir=-1), so a white pawn attacking
	// (r,f) sits one row BELOW at r+1.  Black pawns move toward row 7
	// (dir=+1), so a black pawn attacking (r,f) sits one row ABOVE at r-1.
	pd := -1 // byColor="b": look at r-1
	if byColor == "w" {
		pd = 1 // byColor="w": look at r+1
	}
	for _, df := range []int{-1, 1} {
		pr, pf := r+pd, f+df
		if inBounds(pr, pf) && b[pr][pf] != nil && b[pr][pf].Color == byColor && b[pr][pf].Type == "P" {
			return true
		}
	}
	knightMoves := [][2]int{{-2, -1}, {-2, 1}, {-1, -2}, {-1, 2}, {1, -2}, {1, 2}, {2, -1}, {2, 1}}
	for _, nm := range knightMoves {
		nr, nf := r+nm[0], f+nm[1]
		if inBounds(nr, nf) && b[nr][nf] != nil && b[nr][nf].Color == byColor && b[nr][nf].Type == "N" {
			return true
		}
	}
	for dr := -1; dr <= 1; dr++ {
		for df := -1; df <= 1; df++ {
			if dr == 0 && df == 0 {
				continue
			}
			kr, kf := r+dr, f+df
			if inBounds(kr, kf) && b[kr][kf] != nil && b[kr][kf].Color == byColor && b[kr][kf].Type == "K" {
				return true
			}
		}
	}
	for _, dir := range [][2]int{{0, 1}, {0, -1}, {1, 0}, {-1, 0}} {
		cr, cf := r+dir[0], f+dir[1]
		for inBounds(cr, cf) {
			p := b[cr][cf]
			if p != nil {
				if p.Color == byColor && (p.Type == "R" || p.Type == "Q") {
					return true
				}
				break
			}
			cr += dir[0]
			cf += dir[1]
		}
	}
	for _, dir := range [][2]int{{1, 1}, {1, -1}, {-1, 1}, {-1, -1}} {
		cr, cf := r+dir[0], f+dir[1]
		for inBounds(cr, cf) {
			p := b[cr][cf]
			if p != nil {
				if p.Color == byColor && (p.Type == "B" || p.Type == "Q") {
					return true
				}
				break
			}
			cr += dir[0]
			cf += dir[1]
		}
	}
	return false
}

func isInCheck(b Board, color string) bool {
	kr, kf, ok := getKingPos(b, color)
	if !ok {
		return false
	}
	return isSquareAttacked(b, kr, kf, opp(color))
}

func getPieceMoves(b Board, r, f int, checkLegality bool) [][2]int {
	p := b[r][f]
	if p == nil {
		return nil
	}
	var moves [][2]int
	oppColor := opp(p.Color)

/* 	tryAdd := func(tr, tf int) {
		if !inBounds(tr, tf) {
			return
		}
		if b[tr][tf] != nil && b[tr][tf].Color == p.Color {
			return
		}
		if checkLegality {
			nb := cloneBoard(b)
			nb[tr][tf] = nb[r][f]
			nb[r][f] = nil
			if !isInCheck(nb, p.Color) {
				moves = append(moves, [2]int{tr, tf})
			}
		} else {
			moves = append(moves, [2]int{tr, tf})
		}
	} */

    tryAdd := func(tr, tf int) {
        if !inBounds(tr, tf) {
            return
        }
        if b[r][f] != nil && b[tr][tf] != nil && b[tr][tf].Color == p.Color {
            return
        }
        if checkLegality {
            captured, promoted := applyMoveInPlace(&b, Move{[2]int{r, f}, [2]int{tr, tf}})
            legal := !isInCheck(b, p.Color)
            undoMoveInPlace(&b, Move{[2]int{r, f}, [2]int{tr, tf}}, captured, promoted)
            if legal {
                moves = append(moves, [2]int{tr, tf})
            }
        } else {
            moves = append(moves, [2]int{tr, tf})
        }
    }

	slide := func(dirs [][2]int) {
		for _, d := range dirs {
			cr, cf := r+d[0], f+d[1]
			for inBounds(cr, cf) {
				if b[cr][cf] != nil {
					if b[cr][cf].Color == oppColor {
						tryAdd(cr, cf)
					}
					break
				}
				tryAdd(cr, cf)
				cr += d[0]
				cf += d[1]
			}
		}
	}

	switch p.Type {
	case "P":
		dir := -1
		start := 6
		if p.Color == "b" {
			dir = 1
			start = 1
		}
		if inBounds(r+dir, f) && b[r+dir][f] == nil {
			tryAdd(r+dir, f)
			if r == start && b[r+dir][f] == nil && b[r+2*dir][f] == nil {
				tryAdd(r+2*dir, f)
			}
		}
		for _, df := range []int{-1, 1} {
			if inBounds(r+dir, f+df) && b[r+dir][f+df] != nil && b[r+dir][f+df].Color == oppColor {
				tryAdd(r+dir, f+df)
			}
		}
	case "N":
		for _, nm := range [][2]int{{-2, -1}, {-2, 1}, {-1, -2}, {-1, 2}, {1, -2}, {1, 2}, {2, -1}, {2, 1}} {
			tryAdd(r+nm[0], f+nm[1])
		}
	case "B":
		slide([][2]int{{1, 1}, {1, -1}, {-1, 1}, {-1, -1}})
	case "R":
		slide([][2]int{{0, 1}, {0, -1}, {1, 0}, {-1, 0}})
	case "Q":
		slide([][2]int{{1, 1}, {1, -1}, {-1, 1}, {-1, -1}, {0, 1}, {0, -1}, {1, 0}, {-1, 0}})
	case "K":
		for dr := -1; dr <= 1; dr++ {
			for df := -1; df <= 1; df++ {
				if dr != 0 || df != 0 {
					tryAdd(r+dr, f+df)
				}
			}
		}
	}
	return moves
}

func getAllMoves(b Board, color string) []Move {
	var moves []Move
	for r := 0; r < 8; r++ {
		for f := 0; f < 8; f++ {
			if b[r][f] != nil && b[r][f].Color == color {
				pm := getPieceMoves(b, r, f, true)
				for _, to := range pm {
					moves = append(moves, Move{From: [2]int{r, f}, To: to})
				}
			}
		}
	}
	return moves
}

// getLegalMoves filters getAllMoves to only moves that do not leave the
// moving side's King in check after the move is applied.
// This is the correct set for game play and lookahead — a player must
// resolve check before doing anything else, and cannot move pinned pieces
// in ways that expose the King.
func getLegalMoves(b Board, color string) []Move {
	var legal []Move
	for _, m := range getAllMoves(b, color) {
		nb := applyMove(b, m)
		if !isInCheck(nb, color) {
			legal = append(legal, m)
		}
	}
	return legal
}

func boardPower(b Board, color string) float64 {
	var t float64
	for r := 0; r < 8; r++ {
		for f := 0; f < 8; f++ {
			if b[r][f] != nil && b[r][f].Color == color {
				t += pieceValue[b[r][f].Type]
			}
		}
	}
	return t
}

func threatProximity(b Board, color string) float64 {
	oppColor := opp(color)
	kr, kf, ok := getKingPos(b, oppColor)
	if !ok {
		return 1.0
	}
	var score float64
	for r := 0; r < 8; r++ {
		for f := 0; f < 8; f++ {
			p := b[r][f]
			if p == nil || p.Color != color {
				continue
			}
			// Skip the King — pieceThreat["K"]=0 so it contributes nothing,
			// but more importantly we don't want king advances clearing paths
			// that inflate other pieces' proximity scores (Gemini fix).
			if p.Type == "K" {
				continue
			}
			moves := getPieceMoves(b, r, f, false)
			for _, m := range moves {
				dr := m[0] - kr
				if dr < 0 {
					dr = -dr
				}
				df := m[1] - kf
				if df < 0 {
					df = -df
				}
				dist := dr
				if df > dist {
					dist = df
				}
				pt := pieceThreat[p.Type]
				if dist == 0 {
					score += pt * 3.0
				} else if dist == 1 {
					score += pt * 1.5
				} else if dist <= 3 {
					score += pt * (0.5 / float64(dist))
				}
			}
		}
	}
	oppMoves := len(getPieceMoves(b, kr, kf, true))
	escapePenalty := 1.0 - float64(oppMoves)/8.0
	if escapePenalty < 0 {
		escapePenalty = 0
	}
	score += escapePenalty * 2.0
	if isInCheck(b, oppColor) {
		score += 5.0
	}
	if score > 15.0 {
		return 1.0
	}
	return score / 15.0
}

// kingFreedom returns raw escape-square count (0-1).
// Still used as a minor tiebreaker — kept but weighted low in scoreMove.
func kingFreedom(b Board, color string) float64 {
	kr, kf, ok := getKingPos(b, color)
	if !ok {
		return 0
	}
	return float64(len(getPieceMoves(b, kr, kf, true))) / 8.0
}

// kingSafety measures how well the King is shielded by its own pieces.
// Friendly pieces at Chebyshev distance 1 score 3 each; distance 2 score 1.
// Result normalised 0–1 (max ≈ 24 raw points → clamp at 1.0).
//
// This replaces raw kingFreedom as the primary safety signal because it
// rewards the King staying behind its own pieces rather than marching
// into the centre — no piece-type if/then rules needed, the geometry
// does the work: a King that advances loses friendly proximity naturally.
func kingSafety(b Board, color string) float64 {
	kr, kf, ok := getKingPos(b, color)
	if !ok {
		return 0
	}
	shield := 0
	for dr := -2; dr <= 2; dr++ {
		for df := -2; df <= 2; df++ {
			if dr == 0 && df == 0 {
				continue
			}
			r, f := kr+dr, kf+df
			if r < 0 || r > 7 || f < 0 || f > 7 {
				continue
			}
			p := b[r][f]
			if p != nil && p.Color == color {
				// Chebyshev distance: max of abs row/col deltas
				d := dr
				if d < 0 {
					d = -d
				}
				if df < 0 {
					if -df > d {
						d = -df
					}
				} else if df > d {
					d = df
				}
				if d == 1 {
					shield += 3 // adjacent — strong shield
				} else {
					shield += 1 // one step out — softer cover
				}
			}
		}
	}
	// Divisor 24 (not 12): removes the plateau where ≥4 shield pieces all
	// scored 1.0 identically — every piece lost now costs measurably.
	// Clamp removed for same reason: more shield = strictly higher score.
	return float64(shield) / 24.0
}

// gravityDanger measures the continuous gravitational threat that enemy pieces
// exert on our King, regardless of whether they have a legal move this turn.
// Uses piece value / Chebyshev distance — a Queen 5 squares away registers
// strongly even if currently blocked, fixing the move-list blind spot in
// threatProximity (Gemini fix: sensory blindness to long-range sliding attacks).
// Result normalised ~0-1 (raw scores typically 0-15, clamped at 1.0).
func gravityDanger(b Board, color string) float64 {
	oppColor := opp(color)
	kr, kf, ok := getKingPos(b, color)
	if !ok {
		return 0
	}
	var field float64
	for r := 0; r < 8; r++ {
		for f := 0; f < 8; f++ {
			p := b[r][f]
			if p == nil || p.Color != oppColor {
				continue
			}
			dr := r - kr
			if dr < 0 {
				dr = -dr
			}
			df := f - kf
			if df < 0 {
				df = -df
			}
			dist := dr
			if df > dist {
				dist = df
			}
			if dist == 0 {
				dist = 1
			}
			field += pieceValue[p.Type] / float64(dist)
		}
	}
	v := field / 15.0
	if v > 1.0 {
		return 1.0
	}
	return v
}

func applyMove(b Board, m Move) Board {
	nb := cloneBoard(b)
	nb[m.To[0]][m.To[1]] = nb[m.From[0]][m.From[1]]
	nb[m.From[0]][m.From[1]] = nil
	// Pawn promotion
	p := nb[m.To[0]][m.To[1]]
	if p != nil && p.Type == "P" && (m.To[0] == 0 || m.To[0] == 7) {
		nb[m.To[0]][m.To[1]] = &Piece{p.Color, "Q"}
	}
	return nb
}