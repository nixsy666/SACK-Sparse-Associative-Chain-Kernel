package main

import (
	"encoding/json"
	"fmt"
	"log"
	"math"
	"math/rand"
	"net/http"
	"os"
	"sync"
)

// ============================================================================
// CONSTANTS
// ============================================================================
const (
	CheckWin  = 25
	SaveEvery = 10 // write tomb blob every N completed games, not every win
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
	WChecks    int
	BChecks    int
	// Pressure history for adversarial tomb training.
	// After each black move, append pressureSnapshot(board, "w") to WPressureHistory
	// (what white's position looked like while black was building its winning attack).
	// On white loss, this window feeds white's adversarial tomb so it recognises
	// similar losing trajectories in future games without punishing the field.
	WPressureHistory [][CoarseCells]float32
	BPressureHistory [][CoarseCells]float32
}

func freshGameState() *GameState {
	return &GameState{
		Board:      initialBoard(),
		Turn:       "w",
		FirstMoveW: true,
		FirstMoveB: true,
		WFreedom:   1.0,
		BFreedom:   1.0,
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
// scoreMove — v17 emergence equation. True tomb dangle.
//
// The previous version used child.queryField() → resonate() which reads the
// neuron field's Successes/Failures ratio — the old v13 heuristic counter.
// That was NOT the tomb's pattern memory. It was the old system with a new label.
//
// This version directly queries SharedTomb.PeekMatch — the actual stored strategy
// profiles built from winning causal chains. The tomb's memory IS the scoring signal.
//
// Dynamic alpha elasticity:
//   alpha = tombConf (from PeekMatch, 0..1)
//   tombConf == 0 → virgin territory → pure random (five-year-olds explore freely)
//   tombConf > 0  → recognised pattern → tomb steers proportionally to confidence
//
// Two signals only:
//   1. SharedTomb.PeekMatch — earned strategy memory
//   2. AdversarialTomb.PeekMatch — earned loss-pattern avoidance
func scoreMove(board Board, color string, move Move, delegator *DelegatorSack) float64 {
	nb := applyMove(board, move)
	_, _, kingOk := getKingPos(nb, color)
	if !kingOk {
		return math.Inf(-1)
	}

	pieceIdx := -1
	if p := board[move.From[0]][move.From[1]]; p != nil {
		pieceIdx = pieceChildIndex(p.Type)
	}

	// Board-agnostic spiral position (still computed — used for addLink in doTick,
	// not for scoring here, but keep consistent with learning phase)
	isCapture := board[move.To[0]][move.To[1]] != nil
	isCheck   := isInCheck(nb, opp(color))
	_ = moveContextSpiralPos(board, color, move, isCapture, isCheck) // computed in doTick

	// Piece-relative snapshot (shared tomb query) and king-centric (adversarial)
	snapAfterPiece := piecePressureSnapshot(nb, color, move.To[0], move.To[1])
	snapAfterKing  := pressureSnapshot(nb, color)

	// TRUE TOMB DANGLE — query the tomb's actual stored strategy profiles directly.
	// ComboResonance ^ pieceAngle gives piece-type-differentiated resonance key:
	// same board state + same piece type → strong match, different piece → partial.
	var tombConf float64
	if pieceIdx >= 0 && delegator.SharedTomb != nil {
		rotatedKey := delegator.ComboResonance ^ pieceAngle(pieceIdx)
		tombConf = delegator.SharedTomb.PeekMatch(rotatedKey, snapAfterPiece)
	}

	// Dynamic alpha: tombConf drives how much the tomb steers vs. random exploration.
	// At game 1 with empty tomb, tombConf ≈ 0 → pure random (naive).
	// After N games, confident patterns steer; unknown territory still randomises.
	alpha := tombConf
	baseRandom := rand.Float64() // full 0..1 spread — five-year-olds make real random choices

	score := (1.0-alpha)*baseRandom + alpha*tombConf

	// Adversarial penalty: moves leading toward known losing board states are penalised.
	// Direct PeekMatch on AdversarialTomb — same pattern-memory approach.
	if pieceIdx >= 0 && delegator.AdversarialTomb != nil {
		adversarialConf := delegator.AdversarialTomb.PeekMatch(
			delegator.ComboResonance^pieceAngle(pieceIdx), snapAfterKing)
		score -= adversarialConf * 2.0
	}

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

	// Piece-relative pressure snapshot at the source square — used as the
	// "before" context for the chain link. Computed before routeMove so we
	// still have the piece on its original square.
	// Note: we don't know chosenMove yet, so we defer this to after selection.

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
			best := math.Inf(-1)
			for _, m := range allMoves {
				total := scoreMove(gs.Board, color, m, sack)
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
	// Piece-relative snapshot taken at source square before the move lands.
	// Skip for Stockfish turns (that colour's SACK doesn't learn SF's choices).
	// -----------------------------------------------------------------------
	var activeChild *SACKEngine
	var snapBeforePiece [CoarseCells]float32
	if !isStockfishTurn {
		fr, fc := chosenMove.From[0], chosenMove.From[1]
		snapBeforePiece = piecePressureSnapshot(gs.Board, color, fr, fc)
		activeChild, _ = sack.routeMove(gs.Board, chosenMove)
	}

	captured := gs.Board[chosenMove.To[0]][chosenMove.To[1]]
	preMoveBoard := gs.Board // saved for moveContextSpiralPos (needs pre-move board)
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
	// Check detection for board-agnostic spiral position (capture flag already
	// known from captured != nil above).
	isCheckMove := isInCheck(gs.Board, oppColor)

	// Piece-relative snapshot at destination — for chain link context and shared tomb
	snapAfterPiece := piecePressureSnapshot(gs.Board, color, chosenMove.To[0], chosenMove.To[1])
	// King-centric snapshot — for shared tomb pulse and adversarial history
	snapAfterKing := pressureSnapshot(gs.Board, color)

	// Board-agnostic spiral position (same move class → same position regardless of board coordinates)
	spiralPos := moveContextSpiralPos(preMoveBoard, color, chosenMove, captured != nil, isCheckMove)

	var conf float64
	if !isStockfishTurn && activeChild != nil {
		var nid int
		nid, conf = activeChild.queryField(spiralPos)
		activeChild.LastConf = conf
		sack.LastConf = conf
		sack.addLink(nid, spiralPos, snapBeforePiece, snapAfterPiece)
	}
	// Pulse shared tomb regardless — SACK observes Stockfish's resulting
	// board state so it builds up pressure context for future turns.
	sfIdx := sack.ActiveIdx
	if isStockfishTurn {
		sfIdx = -1
	}
	sack.pulseSharedTomb(sfIdx, snapAfterKing)

	// -----------------------------------------------------------------------
	// ADVERSARIAL PRESSURE HISTORY
	// After each non-Stockfish move, track the board state from the OTHER
	// side's perspective. On loss, this window feeds the adversarial tomb:
	// "the board looked like this (from my view) while the opponent was winning."
	// -----------------------------------------------------------------------
	if !isStockfishTurn {
		if color == "b" {
			// Black just moved — record white's view of this position
			wSnap := pressureSnapshot(gs.Board, "w")
			gs.WPressureHistory = append(gs.WPressureHistory, wSnap)
			if len(gs.WPressureHistory) > 12 {
				gs.WPressureHistory = gs.WPressureHistory[1:]
			}
		} else {
			// White just moved — record black's view of this position
			bSnap := pressureSnapshot(gs.Board, "b")
			gs.BPressureHistory = append(gs.BPressureHistory, bSnap)
			if len(gs.BPressureHistory) > 12 {
				gs.BPressureHistory = gs.BPressureHistory[1:]
			}
		}
	}

	_ = sfApiResp // used below for result fields

	// Freedom — display metric only, not used in scoring
	freedom := kingFreedom(gs.Board, color)
	prevFreedom := gs.WFreedom
	if color == "b" {
		prevFreedom = gs.BFreedom
	}
	if color == "w" {
		gs.WFreedom = freedom
	} else {
		gs.BFreedom = freedom
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
		result.WDelta = freedom - prevFreedom
	} else {
		gs.BLastMove = &chosenMove
		result.BCoilPos = coilPosStr
		result.BConf = conf
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

		// Mid-game capture reinforcement — flat pulse to signal "this chain
		// included a capture and the game continued." Flat weight only: the
		// tomb learns which captures matter most through chain density, not
		// through injected piece-value ordering.
		if !isStockfishTurn && captured.Type != "K" {
			sack.reinforceCapture(0.3)
		}

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

	// Check detection — win condition only; no artificial reinforcement signal.
	// The tomb discovers that checks are valuable through causal chain experience:
	// games that include many checks before winning produce dense, well-matched
	// chains. No explicit "check = good" signal needed.
	if !gs.GameOver && isInCheck(gs.Board, oppColor) {
		if color == "w" {
			gs.WChecks++
		} else {
			gs.BChecks++
		}
		addLog(color+" CHECKS "+oppColor+" [W:"+fmt.Sprintf("%d",gs.WChecks)+" B:"+fmt.Sprintf("%d",gs.BChecks)+"]", "check")
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
		losePressureHistory := gs.WPressureHistory // loser = white by default
		if result.Winner == "b" {
			winSack = srv.sackB
			loseSack = srv.sackW
			losePressureHistory = gs.BPressureHistory
		}
		// Winner: reinforce full causal chain — density in winning positions
		winSack.reinforceOutcome(1)
		// Loser: no punishment cascade — store adversarial pattern instead.
		// The loser's chain is cleared without degrading any field values.
		// Loss learning comes from the adversarial tomb recognising similar
		// board states and penalising candidate moves that head there.
		loseSack.storeAdversarial(losePressureHistory)
		loseSack.reinforceOutcome(-1) // clears chain only; no dent/dull in v17
		addLog("✦ "+result.Winner+" WINS — W CHAINS:"+fmt.Sprintf("%d",srv.sackW.totalChains())+" B CHAINS:"+fmt.Sprintf("%d",srv.sackB.totalChains()), "win")
		// Save every SaveEvery completed games — not after every win.
		// Disk I/O for the unified blob is substantial; gating it keeps the
		// processing engine free to cycle through games at full throughput.
		totalGames := srv.wWins + srv.bWins
		if totalGames > 0 && totalGames%SaveEvery == 0 {
			srv.maybeSave()
		}
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