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

// pieceValue — geometric encoding weight for pressure snapshots ONLY.
// v17 EMERGENCE TEST: all pieces set to 1.0 (uniform).
// The tomb must discover that the queen is valuable through felt absence —
// the density of winning chains that include queens, not a number we inject.
// When the queen is lost, the chains that included her stop firing. Over hundreds
// of games, the tomb learns the structural difference through pure experience.
// Do NOT restore asymmetric weights until the emergence test is complete.
var pieceValue = map[string]float64{"K": 1.0, "Q": 1.0, "R": 1.0, "B": 1.0, "N": 1.0, "P": 1.0}

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

func applyMoveInPlace(b *Board, m Move) (captured *Piece, promoted bool) {
	captured = b[m.To[0]][m.To[1]]
	b[m.To[0]][m.To[1]] = b[m.From[0]][m.From[1]]
	b[m.From[0]][m.From[1]] = nil

	p := b[m.To[0]][m.To[1]]
	if p != nil && p.Type == "P" && (m.To[0] == 0 || m.To[0] == 7) {
		b[m.To[0]][m.To[1]] = &Piece{p.Color, "Q"}
		promoted = true
	}
	return
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
	// Pawn attacks
	pd := -1
	if byColor == "w" {
		pd = 1
	}
	for _, df := range []int{-1, 1} {
		pr, pf := r+pd, f+df
		if inBounds(pr, pf) && b[pr][pf] != nil &&
			b[pr][pf].Color == byColor && b[pr][pf].Type == "P" {
			return true
		}
	}

	// Knights
	for _, nm := range [][2]int{{-2, -1}, {-2, 1}, {-1, -2}, {-1, 2}, {1, -2}, {1, 2}, {2, -1}, {2, 1}} {
		nr, nf := r+nm[0], f+nm[1]
		if inBounds(nr, nf) && b[nr][nf] != nil &&
			b[nr][nf].Color == byColor && b[nr][nf].Type == "N" {
			return true
		}
	}

	// King adjacency
	for dr := -1; dr <= 1; dr++ {
		for df := -1; df <= 1; df++ {
			if dr == 0 && df == 0 {
				continue
			}
			kr, kf := r+dr, f+df
			if inBounds(kr, kf) && b[kr][kf] != nil &&
				b[kr][kf].Color == byColor && b[kr][kf].Type == "K" {
				return true
			}
		}
	}

	// Rook/Queen (orthogonal)
	for _, d := range [][2]int{{1, 0}, {-1, 0}, {0, 1}, {0, -1}} {
		cr, cf := r+d[0], f+d[1]
		for inBounds(cr, cf) {
			p := b[cr][cf]
			if p != nil {
				if p.Color == byColor && (p.Type == "R" || p.Type == "Q") {
					return true
				}
				break
			}
			cr += d[0]
			cf += d[1]
		}
	}

	// Bishop/Queen (diagonal)
	for _, d := range [][2]int{{1, 1}, {1, -1}, {-1, 1}, {-1, -1}} {
		cr, cf := r+d[0], f+d[1]
		for inBounds(cr, cf) {
			p := b[cr][cf]
			if p != nil {
				if p.Color == byColor && (p.Type == "B" || p.Type == "Q") {
					return true
				}
				break
			}
			cr += d[0]
			cf += d[1]
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

	tryAdd := func(tr, tf int) {
		if !inBounds(tr, tf) {
			return
		}
		if b[tr][tf] != nil && b[tr][tf].Color == p.Color {
			return
		}

		if !checkLegality {
			moves = append(moves, [2]int{tr, tf})
			return
		}

		// simulate safely
		nb := cloneBoard(b)
		cap, promo := applyMoveInPlace(&nb, Move{[2]int{r, f}, [2]int{tr, tf}})
		_ = cap
		_ = promo

		// king cannot move into check
		if !isInCheck(nb, p.Color) {
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
			if r == start && b[r+2*dir][f] == nil {
				tryAdd(r+2*dir, f)
			}
		}
		for _, df := range []int{-1, 1} {
			if inBounds(r+dir, f+df) &&
				b[r+dir][f+df] != nil &&
				b[r+dir][f+df].Color == oppColor {
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
		slide([][2]int{{1, 0}, {-1, 0}, {0, 1}, {0, -1}})

	case "Q":
		slide([][2]int{
			{1, 1}, {1, -1}, {-1, 1}, {-1, -1},
			{1, 0}, {-1, 0}, {0, 1}, {0, -1},
		})

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
				for _, to := range getPieceMoves(b, r, f, true) {
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
	return getAllMoves(b, color)
}

// kingFreedom returns raw escape-square count (0-1).
// Display metric only — not used in scoring. The tomb discovers king safety
// through causal chain experience, not programmed heuristics.
func kingFreedom(b Board, color string) float64 {
	kr, kf, ok := getKingPos(b, color)
	if !ok {
		return 0
	}
	return float64(len(getPieceMoves(b, kr, kf, true))) / 8.0
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
