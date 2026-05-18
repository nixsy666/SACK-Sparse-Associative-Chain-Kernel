package main

import (
	"encoding/json"
	"fmt"
	"log"
	"math"
	"math/rand"
	"net/http"
	"os"
	"sort"
	"sync"
)

// ============================================================================
// CONSTANTS
// ============================================================================
const (
	CheckWin  = 25
	SaveEvery = 1
)

// elasticLookaheadParams returns (maxDepth, sampleSizes) based on confidence.
// High confidence → shallow/narrow. Low confidence → deep/wide.
func elasticLookaheadParams(conf float64) (int, [4]int) {
	// conf 0..1
	if conf >= 0.7 {
		// High confidence — shallow, narrow
		return 2, [4]int{3, 2, 0, 0}
	} else if conf >= 0.4 {
		// Medium
		return 3, [4]int{5, 3, 2, 0}
	}
	// Low confidence — deep, wide
	return 4, [4]int{6, 4, 3, 2}
}

const (
	D2W = 0.50
	D3W = 0.25
	D4W = 0.15
	D5W = 0.08
)

// ============================================================================
// GAME STATE
// ============================================================================
type GameState struct {
	Board      Board
	Turn       string
	MoveCount  int
	FirstMoveW bool
	FirstMoveB bool
	GameOver   bool
	WCaptured  []string
	BCaptured  []string
	WFreedom   float64
	BFreedom   float64
	WLastMove  *Move
	BLastMove  *Move
	WVisited   map[string]int
	BVisited   map[string]int
	WChecks    int
	BChecks    int
}

func freshGameState() *GameState {
	return &GameState{
		Board:      initialBoard(),
		Turn:       "w",
		FirstMoveW: true,
		FirstMoveB: true,
		WFreedom:   1.0,
		BFreedom:   1.0,
		WVisited:   make(map[string]int),
		BVisited:   make(map[string]int),
	}
}

// ============================================================================
// SERVER STATE
// ============================================================================
type Server struct {
	mu          sync.Mutex
	state       *GameState
	sackW       *DelegatorSack
	sackB       *DelegatorSack
	generations int
	wWins       int
	bWins       int
	autoRun     bool
	autoTarget  int
	autoStart   int
	saveFile    string

	// Chess-API / Stockfish integration
	mode         string  // "self_play" | "vs_stockfish"
	sfSide       string  // which colour Stockfish plays ("w" or "b")
	lastSFEval   float64 // last Stockfish centipawn eval (÷100)
	lastWinChance float64 // last Stockfish win-chance for white (0–100)
}

// ============================================================================
// MOVE SCORING
// ============================================================================
// scoreMove routes to the piece-type child sack for this move, giving each
// piece type its own learned field to evaluate positions from.
func scoreMove(board Board, color string, move Move, delegator *DelegatorSack, visited map[string]int) float64 {
	nb := applyMove(board, move)
	oppColor := opp(color)
	_, _, kingOk := getKingPos(nb, color)
	if !kingOk {
		return math.Inf(-1)
	}

	// King-centric pressure for spiral position mapping
	snapBefore := pressureSnapshot(board, color)
	snapAfter  := pressureSnapshot(nb, color)
	spiralPos  := deltaToSpiralPos(snapBefore, snapAfter)

	// Soft-route to the piece child for this move (no ownership update)
	child := delegator.peekMove(board, move)
	var conf float64
	if child != nil {
		_, conf = child.queryField(spiralPos)
	}

	score := conf * 5.0
	score += threatProximity(nb, color) * 8.0

	// kingSafety weighted by game phase: heavy in midgame (king must hide),
	// decays in endgame (king should activate). Continuous, no if-then switch.
	ksW := phaseKingSafetyWeight(nb)
	score += kingSafety(nb, color) * ksW

	// gravityDanger: continuous enemy threat field on our king — piece value / distance.
	// Catches blocked sliding pieces that move-list checks miss entirely.
	score -= gravityDanger(nb, color) * 6.0

	pieceIdx := -1
	if p := board[move.From[0]][move.From[1]]; p != nil {
		pieceIdx = pieceChildIndex(p.Type)
	}

	// Two-layer tomb boost:
	//   Shared tomb (×2.5) — board-level position knowledge, cross-piece bleed.
	//   Child tomb (×1.5) — piece-specific sequence knowledge, high precision.
	if pieceIdx >= 0 {
		sharedConf := delegator.sharedTombConf(pieceIdx, snapAfter)
		score += sharedConf * 2.5

		// Continuous diversity pressure — pieces used below their fair share
		// (1/6 of all routes) get a proportional bonus, keeping all piece
		// types active throughout training rather than just on first use.
		totalRoutes := 0
		for _, rc := range delegator.ChildRouteCounts {
			totalRoutes += rc
		}
		if totalRoutes > 0 {
			actualShare := float64(delegator.ChildRouteCounts[pieceIdx]) / float64(totalRoutes)
			fairShare := 1.0 / 6.0
			if actualShare < fairShare {
				score += (fairShare - actualShare) * 12.0
			}
		} else {
			score += 2.0 // first-game flat nudge for all pieces
		}
	}
	if child != nil {
		childConf := child.LastPulse.RecognitionConf
		if child.LastPulse.TopDepth > 4 {
			childConf *= 1.0 + float64(child.LastPulse.TopDepth)*0.1
		}
		score += childConf * 1.5
	}

	if isInCheck(nb, oppColor) {
		score += 10.0
	}

	kr, kf, ok := getKingPos(nb, color)
	if ok {
		vKey := fmt.Sprintf("%d_%d", kr, kf)
		score -= float64(visited[vKey]) * 4.0
	}

	return score
}

// sortMovesByTactics reorders moves so captures and checks come first.
// This ensures that when the lookahead slices to a sample size, it always
// evaluates the most tactically violent replies rather than random back-rank moves.
func sortMovesByTactics(moves []Move, b Board, color string) {
	sort.SliceStable(moves, func(i, j int) bool {
		// Captures first
		iCapture := b[moves[i].To[0]][moves[i].To[1]] != nil
		jCapture := b[moves[j].To[0]][moves[j].To[1]] != nil
		if iCapture != jCapture {
			return iCapture
		}
		return false
	})
}

// phaseKingSafetyWeight returns a kingSafety multiplier that scales with material.
// High material (midgame): weight up to 10.0 — king must stay shielded.
// Low material (endgame): weight decays toward 1.0 — king becomes an active piece.
func phaseKingSafetyWeight(b Board) float64 {
	total := boardPower(b, "w") + boardPower(b, "b")
	// Full board ≈ 78 points. Endgame threshold ≈ 20.
	t := total / 60.0
	if t > 1.0 {
		t = 1.0
	}
	// t=1.0 (midgame) → weight 10.0 | t=0 (endgame) → weight 1.0
	return 1.0 + t*9.0
}

// lookaheadScore routes each ply to the appropriate piece-type child so that
// the specialist for each piece type evaluates its own candidate moves.
func lookaheadScore(board Board, color string, move Move, delegator *DelegatorSack) float64 {
	oppColor := opp(color)

	nb1 := applyMove(board, move)

	snapBase := pressureSnapshot(board, color)
	snap1    := pressureSnapshot(nb1, color)
	sp1      := deltaToSpiralPos(snapBase, snap1)

	child1 := delegator.peekMove(board, move)
	var d1conf float64
	if child1 != nil {
		_, d1conf = child1.queryField(sp1)
	}
	score := d1conf + threatProximity(nb1, color)*D2W + kingSafety(nb1, color)*2.0

	// Elastic depth and sample sizes based on current delegator confidence
	maxDepth, samples := elasticLookaheadParams(delegator.LastConf)

	oppMoves := getLegalMoves(nb1, oppColor)
	if len(oppMoves) == 0 {
		return score + 2.0
	}

	// Depth 2 — best opponent response
	// Pre-sort: captures first so the slice always includes the most dangerous replies.
	sortMovesByTactics(oppMoves, nb1, oppColor)
	sample2 := oppMoves
	if s2 := samples[0]; s2 > 0 && len(sample2) > s2 {
		sample2 = sample2[:s2]
	}

	type scoredMove struct {
		move   Move
		board  Board
		threat float64
	}
	var oppScored []scoredMove
	for _, om := range sample2 {
		nb2 := applyMove(nb1, om)
		// Opponent's score: threat + how much our king is exposed + gravity danger
		t := threatProximity(nb2, oppColor) + (1.0-kingSafety(nb2, color))*2.0 + gravityDanger(nb2, color)*3.0
		oppScored = append(oppScored, scoredMove{om, nb2, t})
	}
	bestCounterIdx := 0
	for i := 1; i < len(oppScored); i++ {
		if oppScored[i].threat > oppScored[bestCounterIdx].threat {
			bestCounterIdx = i
		}
	}
	bestCounter := oppScored[bestCounterIdx]
	score -= bestCounter.threat * D2W

	if maxDepth < 3 {
		return score
	}

	// Depth 3
	ourMoves3 := getLegalMoves(bestCounter.board, color)
	if len(ourMoves3) == 0 {
		return score
	}
	sortMovesByTactics(ourMoves3, bestCounter.board, color)
	sample3 := ourMoves3
	if s3 := samples[1]; s3 > 0 && len(sample3) > s3 {
		sample3 = sample3[:s3]
	}
	bestD3 := math.Inf(-1)
	var bestBoard3 *Board
	for _, m3 := range sample3 {
		nb3 := applyMove(bestCounter.board, m3)
		snap3 := pressureSnapshot(nb3, color)
		sp3   := deltaToSpiralPos(snap1, snap3)
		child3 := delegator.peekMove(bestCounter.board, m3)
		var r3 float64
		if child3 != nil {
			_, r3 = child3.queryField(sp3)
		}
		ksW3 := phaseKingSafetyWeight(nb3)
		t3 := threatProximity(nb3, color) - threatProximity(nb3, oppColor)*0.5 +
			kingSafety(nb3, color)*ksW3 - gravityDanger(nb3, color)*3.0
		s3 := r3 + t3
		if s3 > bestD3 {
			bestD3 = s3
			b := nb3
			bestBoard3 = &b
		}
	}
	score += bestD3 * D3W

	if maxDepth < 4 || bestBoard3 == nil {
		return score
	}

	// Depth 4
	oppMoves4 := getLegalMoves(*bestBoard3, oppColor)
	if len(oppMoves4) == 0 {
		return score
	}
	sortMovesByTactics(oppMoves4, *bestBoard3, oppColor)
	sample4 := oppMoves4
	if s4 := samples[2]; s4 > 0 && len(sample4) > s4 {
		sample4 = sample4[:s4]
	}
	bestOppD4 := math.Inf(-1)
	var bestBoard4 *Board
	for _, m4 := range sample4 {
		nb4 := applyMove(*bestBoard3, m4)
		t4 := threatProximity(nb4, oppColor) + (1.0-kingSafety(nb4, color))*2.0 + gravityDanger(nb4, color)*3.0
		if t4 > bestOppD4 {
			bestOppD4 = t4
			b := nb4
			bestBoard4 = &b
		}
	}
	score -= bestOppD4 * D4W

	if maxDepth < 5 || bestBoard4 == nil {
		return score
	}

	// Depth 5 — only reached at low confidence
	ourMoves5 := getLegalMoves(*bestBoard4, color)
	if len(ourMoves5) == 0 {
		return score
	}
	sortMovesByTactics(ourMoves5, *bestBoard4, color)
	sample5 := ourMoves5
	if s5 := samples[3]; s5 > 0 && len(sample5) > s5 {
		sample5 = sample5[:s5]
	}
	bestD5 := math.Inf(-1)
	for _, m5 := range sample5 {
		nb5 := applyMove(*bestBoard4, m5)
		snap5 := pressureSnapshot(nb5, color)
		sp5   := deltaToSpiralPos(snap1, snap5)
		child5 := delegator.peekMove(*bestBoard4, m5)
		var r5 float64
		if child5 != nil {
			_, r5 = child5.queryField(sp5)
		}
		ksW5 := phaseKingSafetyWeight(nb5)
		t5 := threatProximity(nb5, color) + kingSafety(nb5, color)*ksW5 - gravityDanger(nb5, color)*3.0
		if r5+t5 > bestD5 {
			bestD5 = r5 + t5
		}
	}
	score += bestD5 * D5W

	return score
}

// ============================================================================
// TICK RESULT
// ============================================================================
type TickResult struct {
	Turn        string   `json:"turn"`
	Color       string   `json:"color"`
	Move        *Move    `json:"move"`
	MoveStr     string   `json:"moveStr"`
	Captured    string   `json:"captured"`
	Check       bool     `json:"check"`
	GameOver    bool     `json:"gameOver"`
	Winner      string   `json:"winner"`
	WinReason   string   `json:"winReason"`
	WChecks     int      `json:"wChecks"`
	BChecks     int      `json:"bChecks"`
	WCaptured   []string `json:"wCaptured"`
	BCaptured   []string `json:"bCaptured"`
	WFreedom    float64  `json:"wFreedom"`
	BFreedom    float64  `json:"bFreedom"`
	MoveCount   int      `json:"moveCount"`
	Generations int      `json:"generations"`
	WWins       int      `json:"wWins"`
	BWins       int      `json:"bWins"`
	Board       [8][8]*PieceJSON `json:"board"`
	SackW       SACKStats      `json:"sackW"`   // active piece child (backward compat)
	SackB       SACKStats      `json:"sackB"`
	TombW       TombStats      `json:"tombW"`
	TombB       TombStats      `json:"tombB"`
	DelegW      DelegatorStats `json:"delegW"`  // full delegator view
	DelegB      DelegatorStats `json:"delegB"`
	// SACK signal
	WCoilPos   string  `json:"wCoilPos"`
	BCoilPos   string  `json:"bCoilPos"`
	WConf      float64 `json:"wConf"`
	BConf      float64 `json:"bConf"`
	WThreat    float64 `json:"wThreat"`
	BThreat    float64 `json:"bThreat"`
	WDelta     float64 `json:"wDelta"`
	BDelta     float64 `json:"bDelta"`
	Log        []LogEntry `json:"log"`

	// Stockfish / chess-api fields
	Mode        string  `json:"mode"`
	SFSide      string  `json:"sfSide"`
	IsStockfish bool    `json:"isStockfish"`  // was this tick a Stockfish move?
	SFMove      string  `json:"sfMove"`       // UCI string of Stockfish's move
	SFSAN       string  `json:"sfSan"`        // short algebraic of Stockfish's move
	SFEval      float64 `json:"sfEval"`       // last SF eval (+ = white winning)
	SFWinChance float64 `json:"sfWinChance"`  // last SF win-chance for white 0-100
}

type PieceJSON struct {
	Color string `json:"color"`
	Type  string `json:"type"`
}

type LogEntry struct {
	Msg string `json:"msg"`
	Cls string `json:"cls"`
}

// ============================================================================
// TICK LOGIC
// ============================================================================
func (srv *Server) doTick(sfPreFetch *ChessAPIResponse, sfPreFetchFEN string) TickResult {
	gs := srv.state
	var logs []LogEntry
	addLog := func(msg, cls string) {
		logs = append(logs, LogEntry{msg, cls})
	}

	result := TickResult{
		Turn:      gs.Turn,
		WChecks:   gs.WChecks,
		BChecks:   gs.BChecks,
		MoveCount: gs.MoveCount,
	}

	if gs.GameOver {
		srv.endGeneration()
		result.GameOver = true
		result.Log = logs
		return result
	}

	color := gs.Turn
	oppColor := opp(color)
	sack := srv.sackW
	if color == "b" {
		sack = srv.sackB
	}
	isFirst := gs.FirstMoveW
	if color == "b" {
		isFirst = gs.FirstMoveB
	}

	allMoves := getLegalMoves(gs.Board, color)
	files := []string{"a", "b", "c", "d", "e", "f", "g", "h"}

	if len(allMoves) == 0 {
		if isInCheck(gs.Board, color) {
			addLog("CHECKMATE — "+oppColor+" WINS", "important check")
			gs.GameOver = true
			if oppColor == "w" {
				srv.wWins++
			} else {
				srv.bWins++
			}
			result.Winner = oppColor
			result.WinReason = "checkmate"
		} else {
			addLog("STALEMATE", "important")
			gs.GameOver = true
			sack.reinforceOutcome(0)
			if oppColor == "w" {
				// other sack
				srv.sackB.reinforceOutcome(0)
			} else {
				srv.sackW.reinforceOutcome(0)
			}
		}
		result.GameOver = true
		result.Log = logs
		result.SackW = srv.sackW.activeChildStats()
		result.SackB = srv.sackB.activeChildStats()
		result.DelegW = srv.sackW.stats()
		result.DelegB = srv.sackB.stats()
		return result
	}

	snapBefore := pressureSnapshot(gs.Board, color)

	// -----------------------------------------------------------------------
	// MOVE SELECTION — Stockfish or SACK
	// -----------------------------------------------------------------------
	var chosenMove Move
	isStockfishTurn := srv.mode == "vs_stockfish" && color == srv.sfSide
	promotionPiece  := ""
	var sfApiResp   *ChessAPIResponse

	if isStockfishTurn {
		// Use the result pre-fetched outside the lock.
		// Verify the FEN still matches — a RESET between prefetch and re-acquire
		// would change the board, in which case we skip and fall back to SACK.
		currentFEN := boardToFEN(gs.Board, color, gs.MoveCount)
		resp := sfPreFetch
		if sfPreFetchFEN != currentFEN {
			resp = nil // state changed — stale prefetch, use SACK
		}
		sfOk := false
		if resp != nil && resp.Move != "" {
			sfMove, promo, parseOk := uciToMove(resp.Move)
			if parseOk {
				// Validate against SACK's legal-move list.
				// Castling / en passant moves will fail here and fall back gracefully.
				for _, m := range allMoves {
					if m.From == sfMove.From && m.To == sfMove.To {
						chosenMove    = sfMove
						promotionPiece = promo
						sfApiResp     = resp
						sfOk          = true
						srv.lastSFEval    = resp.Eval
						srv.lastWinChance = resp.WinChance
						label := resp.SAN
						if label == "" {
							label = resp.Move
						}
						addLog(fmt.Sprintf("♟ STOCKFISH: %s  [eval:%.2f  wc:%.1f%%]",
							label, resp.Eval, resp.WinChance), "sf-event")
						break
					}
				}
			}
		}
		if !sfOk {
			if resp == nil {
				addLog("⚠ STOCKFISH UNAVAILABLE — SACK FALLBACK", "important")
			} else {
				addLog("⚠ STOCKFISH MOVE INVALID ("+resp.Move+") — SACK FALLBACK", "important")
			}
			isStockfishTurn = false // fall through to SACK selection
		}
	}

	if !isStockfishTurn {
		if isFirst {
			chosenMove = allMoves[rand.Intn(min(18, len(allMoves)))]
			if color == "w" {
				gs.FirstMoveW = false
			} else {
				gs.FirstMoveB = false
			}
			addLog(color+" OPENING RANDOM", color+"-event")
		} else {
			visited := gs.WVisited
			if color == "b" {
				visited = gs.BVisited
			}
			best := math.Inf(-1)
			for _, m := range allMoves {
				base := scoreMove(gs.Board, color, m, sack, visited)
				la   := lookaheadScore(gs.Board, color, m, sack)
				total := base*0.4 + la*0.6
				if total > best {
					best = total
					chosenMove = m
				}
			}
		}
	}

	// -----------------------------------------------------------------------
	// EXECUTE MOVE
	// Route to piece child BEFORE applying the move — piece is still on From.
	// Skip for Stockfish turns (that colour's SACK doesn't learn SF's choices).
	// -----------------------------------------------------------------------
	var activeChild *SACKEngine
	if !isStockfishTurn {
		activeChild, _ = sack.routeMove(gs.Board, chosenMove)
	}

	captured := gs.Board[chosenMove.To[0]][chosenMove.To[1]]
	gs.Board = applyMove(gs.Board, chosenMove)

	// Apply promotion piece type (Stockfish or pawn-reaching-back-rank)
	if promotionPiece != "" {
		promoMap := map[string]string{"q":"Q","r":"R","b":"B","n":"N"}
		if pt, ok := promoMap[promotionPiece]; ok {
			if p := gs.Board[chosenMove.To[0]][chosenMove.To[1]]; p != nil {
				p.Type = pt
			}
		}
	}

	// -----------------------------------------------------------------------
	// SACK LEARNING — only when SACK made the move
	// -----------------------------------------------------------------------
	snapAfter := pressureSnapshot(gs.Board, color)
	spiralPos := deltaToSpiralPos(snapBefore, snapAfter)
	var conf float64
	if !isStockfishTurn && activeChild != nil {
		var nid int
		nid, conf = activeChild.queryField(spiralPos)
		activeChild.LastConf = conf
		sack.LastConf = conf
		sack.addLink(nid, spiralPos, snapBefore, snapAfter)
	}
	// Pulse shared tomb regardless — SACK observes Stockfish's resulting
	// board state so it builds up pressure context for future turns.
	sfIdx := sack.ActiveIdx
	if isStockfishTurn {
		sfIdx = -1
	}
	sack.pulseSharedTomb(sfIdx, snapAfter)

	_ = sfApiResp // used below for result fields

	// Freedom
	freedom := kingFreedom(gs.Board, color)
	threat  := threatProximity(gs.Board, color)
	prevFreedom := gs.WFreedom
	if color == "b" {
		prevFreedom = gs.BFreedom
	}
	if color == "w" {
		gs.WFreedom = freedom
	} else {
		gs.BFreedom = freedom
	}

	// Visited
	kr, kf, ok := getKingPos(gs.Board, color)
	if ok {
		vKey := fmt.Sprintf("%d_%d", kr, kf)
		if color == "w" {
			gs.WVisited[vKey]++
		} else {
			gs.BVisited[vKey]++
		}
	}

	// Move notation
	ms := files[chosenMove.From[1]] + fmt.Sprintf("%d", 8-chosenMove.From[0]) +
		"→" + files[chosenMove.To[1]] + fmt.Sprintf("%d", 8-chosenMove.To[0])

	// Coil pos string (spiralPos declared in the learning block above)
	coilPosStr := fmt.Sprintf("C%d:%d", spiralCoil(spiralPos), spiralPos)

	if color == "w" {
		gs.WLastMove = &chosenMove
		result.WCoilPos = coilPosStr
		result.WConf = conf
		result.WThreat = threat
		result.WDelta = freedom - prevFreedom
	} else {
		gs.BLastMove = &chosenMove
		result.BCoilPos = coilPosStr
		result.BConf = conf
		result.BThreat = threat
		result.BDelta = freedom - prevFreedom
	}

	if captured != nil {
		cn := map[string]string{"K":"KING","Q":"QUEEN","R":"ROOK","B":"BISHOP","N":"KNIGHT","P":"PAWN"}
		addLog(color+" CAPTURES "+cn[captured.Type], color+"-event")
		if color == "w" {
			gs.WCaptured = append(gs.WCaptured, captured.Type)
		} else {
			gs.BCaptured = append(gs.BCaptured, captured.Type)
		}
		result.Captured = captured.Type
		if captured.Type == "K" {
			addLog(color+" CAPTURES THE KING", "important check")
			gs.GameOver = true
			if color == "w" {
				srv.wWins++
			} else {
				srv.bWins++
			}
			result.Winner = color
			result.WinReason = "king captured"
			result.GameOver = true
		}
	}

	// Check detection
	if !gs.GameOver && isInCheck(gs.Board, oppColor) {
		if color == "w" {
			gs.WChecks++
		} else {
			gs.BChecks++
		}
		addLog(color+" CHECKS "+oppColor+" [W:"+fmt.Sprintf("%d",gs.WChecks)+" B:"+fmt.Sprintf("%d",gs.BChecks)+"]", "check")
		sack.reinforceCheck()
		if gs.WChecks >= CheckWin || gs.BChecks >= CheckWin {
			winner := "w"
			if gs.BChecks >= CheckWin {
				winner = "b"
			}
			addLog("★ "+winner+" WINS BY CHECK DOMINATION!", "win")
			gs.GameOver = true
			if winner == "w" {
				srv.wWins++
			} else {
				srv.bWins++
			}
			result.Winner = winner
			result.WinReason = "check domination"
		}
	}

	if !gs.GameOver {
		gs.MoveCount++
		gs.Turn = oppColor
		if gs.MoveCount > 600 {
			addLog("DRAW — MAX MOVES", "important")
			gs.GameOver = true
			srv.sackW.reinforceOutcome(0)
			srv.sackB.reinforceOutcome(0)
		}
	}

	// Handle game over win reinforcement
	if gs.GameOver && result.Winner != "" {
		winSack := srv.sackW
		loseSack := srv.sackB
		if result.Winner == "b" {
			winSack = srv.sackB
			loseSack = srv.sackW
		}
		winSack.reinforceOutcome(1)
		loseSack.reinforceOutcome(-1)
		addLog("✦ "+result.Winner+" WINS — W CHAINS:"+fmt.Sprintf("%d",srv.sackW.totalChains())+" B CHAINS:"+fmt.Sprintf("%d",srv.sackB.totalChains()), "win")
		srv.maybeSave()
	}

	// Build board JSON
	for r := 0; r < 8; r++ {
		for f := 0; f < 8; f++ {
			if gs.Board[r][f] != nil {
				result.Board[r][f] = &PieceJSON{gs.Board[r][f].Color, gs.Board[r][f].Type}
			}
		}
	}

	result.Turn = gs.Turn
	result.Color = color
	result.Move = &chosenMove
	result.MoveStr = ms
	result.Check = isInCheck(gs.Board, oppColor)
	result.GameOver = gs.GameOver
	result.WChecks = gs.WChecks
	result.BChecks = gs.BChecks
	result.WCaptured = gs.WCaptured
	result.BCaptured = gs.BCaptured
	result.WFreedom = gs.WFreedom
	result.BFreedom = gs.BFreedom
	result.MoveCount = gs.MoveCount
	result.Generations = srv.generations
	result.WWins = srv.wWins
	result.BWins = srv.bWins
	result.SackW = srv.sackW.activeChildStats()
	result.SackB = srv.sackB.activeChildStats()
	result.TombW = srv.sackW.tombStats()
	result.TombB = srv.sackB.tombStats()
	result.DelegW = srv.sackW.stats()
	result.DelegB = srv.sackB.stats()
	result.Log = logs

	// Stockfish fields — always carry last known eval so UI bar stays visible
	result.Mode        = srv.mode
	result.SFSide      = srv.sfSide
	result.IsStockfish = isStockfishTurn
	result.SFEval      = srv.lastSFEval
	result.SFWinChance = srv.lastWinChance
	if sfApiResp != nil {
		result.SFMove = sfApiResp.Move
		result.SFSAN  = sfApiResp.SAN
	}

	return result
}

// Tomb Verification function to give a simple way of seeing it work

func (srv *Server) reportTombStatus() {
	type colorEntry struct {
		label string
		deleg *DelegatorSack
	}
	for _, ce := range []colorEntry{{"White", srv.sackW}, {"Black", srv.sackB}} {
		total := 0
		log.Printf("📊 TOMB STATUS — %s:", ce.label)
		for i, c := range ce.deleg.Children {
			if c == nil {
				continue
			}
			pType := pieceChildOrder[i]
			childTotal := 0
			for _, layer := range c.Tomb.Layers {
				childTotal += len(layer.Chains)
			}
			total += childTotal
			if childTotal > 0 {
				log.Printf("   [%s] %d chains", pType, childTotal)
			}
		}
		if total == 0 {
			log.Printf("   ⚠️  No chains stored yet")
		} else {
			log.Printf("   Total: %d chains across %d piece children", total, ce.deleg.SpawnCount)
		}
	}
}

func (srv *Server) endGeneration() {
	srv.generations++
	srv.sackW.resetChain()
	srv.sackB.resetChain()
	srv.state = freshGameState()
}

func (srv *Server) maybeSave() {
	if srv.saveFile == "" {
		return
	}
	type SaveData struct {
		DelegW      DelegatorSave `json:"delegW"`
		DelegB      DelegatorSave `json:"delegB"`
		Generations int           `json:"generations"`
		WWins       int           `json:"wWins"`
		BWins       int           `json:"bWins"`
	}
	sd := SaveData{
		DelegW:      srv.sackW.serialiseDelegate(),
		DelegB:      srv.sackB.serialiseDelegate(),
		Generations: srv.generations,
		WWins:       srv.wWins,
		BWins:       srv.bWins,
	}
	b, err := json.Marshal(sd)
	if err != nil {
		return
	}
	os.WriteFile(srv.saveFile, b, 0644)
	srv.saveTombs()
}

func (srv *Server) tryLoad() {
	if srv.saveFile == "" {
		return
	}
	b, err := os.ReadFile(srv.saveFile)
	if err != nil {
		return
	}
	type SaveData struct {
		DelegW      DelegatorSave `json:"delegW"`
		DelegB      DelegatorSave `json:"delegB"`
		Generations int           `json:"generations"`
		WWins       int           `json:"wWins"`
		BWins       int           `json:"bWins"`
	}
	var sd SaveData
	if json.Unmarshal(b, &sd) != nil {
		return
	}
	if len(sd.DelegW.Children) > 0 {
		srv.sackW.deserialiseDelegate(sd.DelegW)
	}
	if len(sd.DelegB.Children) > 0 {
		srv.sackB.deserialiseDelegate(sd.DelegB)
	}
	srv.generations = sd.Generations
	srv.wWins = sd.WWins
	srv.bWins = sd.BWins
	log.Printf("Loaded cortex — W:%d B:%d chains (%d+%d piece children), gen %d\n",
		srv.sackW.totalChains(), srv.sackB.totalChains(),
		srv.sackW.SpawnCount, srv.sackB.SpawnCount,
		srv.generations)
	srv.loadTombs()
	srv.reportTombStatus()
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// ============================================================================
// HTTP HANDLERS
// ============================================================================
func cors(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET,POST,OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	w.Header().Set("Content-Type", "application/json")
}

func (srv *Server) handleTick(w http.ResponseWriter, r *http.Request) {
	cors(w)
	if r.Method == "OPTIONS" {
		return
	}

	// --- Pre-fetch Stockfish move OUTSIDE the lock ----------------------------
	// The Stockfish query blocks on UCI I/O (up to StockfishQueryTimeMs).
	// Holding the mutex that long would block every other handler (reset,
	// status, etc.) for the full wait.
	// Instead we: read the minimal state needed → release lock → call API →
	// re-acquire lock → run doTick with the pre-fetched result.
	// Since the client sets pendingTick=true while waiting, no concurrent tick
	// can alter game state between our read and re-acquire.
	var sfPreFetch *ChessAPIResponse
	var sfPreFetchFEN string

	srv.mu.Lock()
	if srv.mode == "vs_stockfish" && !srv.state.GameOver && srv.state.Turn == srv.sfSide {
		sfPreFetchFEN = boardToFEN(srv.state.Board, srv.state.Turn, srv.state.MoveCount)
	}
	srv.mu.Unlock()

	if sfPreFetchFEN != "" {
		sfPreFetch, _ = queryChessAPI(sfPreFetchFEN)
		// error is handled inside doTick — nil sfPreFetch triggers SACK fallback
	}

	srv.mu.Lock()
	result := srv.doTick(sfPreFetch, sfPreFetchFEN)
	srv.mu.Unlock()
	json.NewEncoder(w).Encode(result)
}

func (srv *Server) handleReset(w http.ResponseWriter, r *http.Request) {
	cors(w)
	srv.mu.Lock()
	srv.sackW.resetChain()
	srv.sackB.resetChain()
	srv.state = freshGameState()
	srv.mu.Unlock()
	w.Write([]byte(`{"ok":true}`))
}

func (srv *Server) handleClear(w http.ResponseWriter, r *http.Request) {
	cors(w)
	srv.mu.Lock()
	srv.sackW = newDelegatorSack("White")
	srv.sackB = newDelegatorSack("Black")
	srv.state = freshGameState()
	srv.generations = 0
	srv.wWins = 0
	srv.bWins = 0
	if srv.saveFile != "" {
		os.Remove(srv.saveFile)
	}
	srv.mu.Unlock()
	w.Write([]byte(`{"ok":true}`))
}

func (srv *Server) handleSave(w http.ResponseWriter, r *http.Request) {
	cors(w)
	srv.mu.Lock()
	srv.maybeSave()
	srv.mu.Unlock()
	w.Write([]byte(`{"ok":true}`))
}

type StatusResult struct {
	Generations int            `json:"generations"`
	WWins       int            `json:"wWins"`
	BWins       int            `json:"bWins"`
	SackW       SACKStats      `json:"sackW"`
	SackB       SACKStats      `json:"sackB"`
	TombW       TombStats      `json:"tombW"`
	TombB       TombStats      `json:"tombB"`
	DelegW      DelegatorStats `json:"delegW"`
	DelegB      DelegatorStats `json:"delegB"`
	Board       [8][8]*PieceJSON `json:"board"`
	Turn        string    `json:"turn"`
	WChecks     int       `json:"wChecks"`
	BChecks     int       `json:"bChecks"`
	MoveCount   int       `json:"moveCount"`
	WFreedom    float64   `json:"wFreedom"`
	BFreedom    float64   `json:"bFreedom"`
	WCaptured   []string  `json:"wCaptured"`
	BCaptured   []string  `json:"bCaptured"`
	// Stockfish / mode
	Mode        string  `json:"mode"`
	SFSide      string  `json:"sfSide"`
	SFEval      float64 `json:"sfEval"`
	SFWinChance float64 `json:"sfWinChance"`
}

func (srv *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	cors(w)
	srv.mu.Lock()
	gs := srv.state
	var board [8][8]*PieceJSON
	for rr := 0; rr < 8; rr++ {
		for f := 0; f < 8; f++ {
			if gs.Board[rr][f] != nil {
				board[rr][f] = &PieceJSON{gs.Board[rr][f].Color, gs.Board[rr][f].Type}
			}
		}
	}
	result := StatusResult{
		Generations: srv.generations,
		WWins:       srv.wWins,
		BWins:       srv.bWins,
		SackW:       srv.sackW.activeChildStats(),
		SackB:       srv.sackB.activeChildStats(),
		TombW:       srv.sackW.tombStats(),
		TombB:       srv.sackB.tombStats(),
		DelegW:      srv.sackW.stats(),
		DelegB:      srv.sackB.stats(),
		Board:       board,
		Turn:        gs.Turn,
		WChecks:     gs.WChecks,
		BChecks:     gs.BChecks,
		MoveCount:   gs.MoveCount,
		WFreedom:    gs.WFreedom,
		BFreedom:    gs.BFreedom,
		WCaptured:   gs.WCaptured,
		BCaptured:   gs.BCaptured,
		Mode:        srv.mode,
		SFSide:      srv.sfSide,
		SFEval:      srv.lastSFEval,
		SFWinChance: srv.lastWinChance,
	}
	srv.mu.Unlock()
	json.NewEncoder(w).Encode(result)
}

// handleMode — GET returns current mode; POST sets mode + sfSide.
//
//	GET  /mode
//	POST /mode  body: {"mode":"vs_stockfish","sfSide":"b"}
//	            mode values: "self_play" | "vs_stockfish"
//	            sfSide values: "w" | "b"  (which colour Stockfish plays)
func (srv *Server) handleMode(w http.ResponseWriter, r *http.Request) {
	cors(w)
	if r.Method == "OPTIONS" {
		return
	}
	if r.Method == "GET" {
		srv.mu.Lock()
		json.NewEncoder(w).Encode(map[string]interface{}{
			"mode":        srv.mode,
			"sfSide":      srv.sfSide,
			"sfEval":      srv.lastSFEval,
			"sfWinChance": srv.lastWinChance,
		})
		srv.mu.Unlock()
		return
	}
	// POST
	var req struct {
		Mode   string `json:"mode"`
		SFSide string `json:"sfSide"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Mode == "" {
		http.Error(w, `{"error":"bad request — expected {mode, sfSide}"}`, 400)
		return
	}
	if req.Mode != "self_play" && req.Mode != "vs_stockfish" {
		http.Error(w, `{"error":"unknown mode"}`, 400)
		return
	}
	srv.mu.Lock()
	srv.mode = req.Mode
	if req.SFSide == "w" || req.SFSide == "b" {
		srv.sfSide = req.SFSide
	}
	// Reset eval display on mode switch
	srv.lastSFEval    = 0
	srv.lastWinChance = 50
	srv.mu.Unlock()
	log.Printf("MODE → %s  (Stockfish plays: %s)\n", srv.mode, srv.sfSide)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"ok":     true,
		"mode":   srv.mode,
		"sfSide": srv.sfSide,
	})
}

// ============================================================================
// MAIN
// ============================================================================
func main() {
	initSpiralMap()

	port := "8080"
	if len(os.Args) > 1 {
		port = os.Args[1]
	}
	saveFile := "sack_cortex.json"
	if len(os.Args) > 2 {
		saveFile = os.Args[2]
	}

	srv := &Server{
		state:        freshGameState(),
		sackW:        newDelegatorSack("White"),
		sackB:        newDelegatorSack("Black"),
		saveFile:     saveFile,
		mode:         "self_play",
		sfSide:       "b",   // default: Stockfish plays black, SACK plays white
		lastWinChance: 50.0, // neutral starting eval
	}
	srv.tryLoad()

	http.HandleFunc("/tick",   srv.handleTick)
	http.HandleFunc("/reset",  srv.handleReset)
	http.HandleFunc("/clear",  srv.handleClear)
	http.HandleFunc("/save",   srv.handleSave)
	http.HandleFunc("/status", srv.handleStatus)
	http.HandleFunc("/mode",   srv.handleMode)

	// Serve static files from ./static/
	http.Handle("/", http.FileServer(http.Dir("./static")))

	log.Printf("SACK CHESS v6 — listening on :%s\n", port)
	log.Printf("Save file: %s\n", saveFile)
	log.Printf("Field size: %d | Coils: %d | Neurons: %d\n", fieldSize(), SpiralCoils, NeuronCount)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}