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
	sackW       *SACKEngine
	sackB       *SACKEngine
	generations int
	wWins       int
	bWins       int
	autoRun     bool
	autoTarget  int
	autoStart   int
	saveFile    string
}

// ============================================================================
// MOVE SCORING
// ============================================================================
func scoreMove(board Board, color string, move Move, sack *SACKEngine, visited map[string]int) float64 {
	nb := applyMove(board, move)
	oppColor := opp(color)
	_, _, kingOk := getKingPos(nb, color)
	if !kingOk {
		return math.Inf(-1)
	}

	snapBefore := pressureSnapshot(board, color)
	snapAfter  := pressureSnapshot(nb, color)
	spiralPos  := deltaToSpiralPos(snapBefore, snapAfter)
	_, conf    := sack.queryField(spiralPos)

	score := conf * 5.0
	score += threatProximity(nb, color) * 8.0
	score += kingFreedom(nb, color) * 3.0

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

func lookaheadScore(board Board, color string, move Move, sack *SACKEngine) float64 {
	oppColor := opp(color)

	nb1 := applyMove(board, move)

	snapBase := pressureSnapshot(board, color)
	snap1    := pressureSnapshot(nb1, color)
	sp1      := deltaToSpiralPos(snapBase, snap1)
	_, d1conf := sack.queryField(sp1)
	score := d1conf + threatProximity(nb1, color)*D2W

	// Elastic depth and sample sizes based on current confidence
	maxDepth, samples := elasticLookaheadParams(sack.LastConf)

	oppMoves := getAllMoves(nb1, oppColor)
	if len(oppMoves) == 0 {
		return score + 2.0
	}

	// Depth 2 — best opponent response
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
		t := threatProximity(nb2, oppColor)
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
	ourMoves3 := getAllMoves(bestCounter.board, color)
	if len(ourMoves3) == 0 {
		return score
	}
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
		_, r3 := sack.queryField(sp3)
		t3 := threatProximity(nb3, color) - threatProximity(nb3, oppColor)*0.5
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
	oppMoves4 := getAllMoves(*bestBoard3, oppColor)
	if len(oppMoves4) == 0 {
		return score
	}
	sample4 := oppMoves4
	if s4 := samples[2]; s4 > 0 && len(sample4) > s4 {
		sample4 = sample4[:s4]
	}
	bestOppD4 := math.Inf(-1)
	var bestBoard4 *Board
	for _, m4 := range sample4 {
		nb4 := applyMove(*bestBoard3, m4)
		t4 := threatProximity(nb4, oppColor)
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
	ourMoves5 := getAllMoves(*bestBoard4, color)
	if len(ourMoves5) == 0 {
		return score
	}
	sample5 := ourMoves5
	if s5 := samples[3]; s5 > 0 && len(sample5) > s5 {
		sample5 = sample5[:s5]
	}
	bestD5 := math.Inf(-1)
	for _, m5 := range sample5 {
		nb5 := applyMove(*bestBoard4, m5)
		snap5 := pressureSnapshot(nb5, color)
		sp5   := deltaToSpiralPos(snap1, snap5)
		_, r5 := sack.queryField(sp5)
		t5 := threatProximity(nb5, color)
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
	SackW       SACKStats `json:"sackW"`
	SackB       SACKStats `json:"sackB"`
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
func (srv *Server) doTick() TickResult {
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

	allMoves := getAllMoves(gs.Board, color)
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
		result.SackW = srv.sackW.stats()
		result.SackB = srv.sackB.stats()
		return result
	}

	snapBefore := pressureSnapshot(gs.Board, color)

	var chosenMove Move
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

	// Execute move
	captured := gs.Board[chosenMove.To[0]][chosenMove.To[1]]
	gs.Board = applyMove(gs.Board, chosenMove)

	// SACK chain link
	snapAfter := pressureSnapshot(gs.Board, color)
	spiralPos := deltaToSpiralPos(snapBefore, snapAfter)
	nid, conf := sack.queryField(spiralPos)
	sack.LastConf = conf
	sack.addLink(nid, spiralPos, snapBefore, snapAfter)

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

	// Coil pos string
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
		addLog("✦ "+result.Winner+" WINS — W CHAINS:"+fmt.Sprintf("%d",srv.sackW.TotalChains)+" B CHAINS:"+fmt.Sprintf("%d",srv.sackB.TotalChains), "win")
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
	result.SackW = srv.sackW.stats()
	result.SackB = srv.sackB.stats()
	result.Log = logs

	return result
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
		NeuronsW    []NeuronData `json:"neuronsW"`
		NeuronsB    []NeuronData `json:"neuronsB"`
		Generations int          `json:"generations"`
		WWins       int          `json:"wWins"`
		BWins       int          `json:"bWins"`
	}
	sd := SaveData{
		NeuronsW:    srv.sackW.serialise(),
		NeuronsB:    srv.sackB.serialise(),
		Generations: srv.generations,
		WWins:       srv.wWins,
		BWins:       srv.bWins,
	}
	b, err := json.Marshal(sd)
	if err != nil {
		return
	}
	os.WriteFile(srv.saveFile, b, 0644)
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
		NeuronsW    []NeuronData `json:"neuronsW"`
		NeuronsB    []NeuronData `json:"neuronsB"`
		Generations int          `json:"generations"`
		WWins       int          `json:"wWins"`
		BWins       int          `json:"bWins"`
	}
	var sd SaveData
	if json.Unmarshal(b, &sd) != nil {
		return
	}
	if sd.NeuronsW != nil {
		srv.sackW.deserialise(sd.NeuronsW)
	}
	if sd.NeuronsB != nil {
		srv.sackB.deserialise(sd.NeuronsB)
	}
	srv.generations = sd.Generations
	srv.wWins = sd.WWins
	srv.bWins = sd.BWins
	log.Printf("Loaded cortex — W:%d B:%d chains, gen %d\n",
		srv.sackW.TotalChains, srv.sackB.TotalChains, srv.generations)
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
	srv.mu.Lock()
	result := srv.doTick()
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
	srv.sackW = newSACK("White")
	srv.sackB = newSACK("Black")
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
	Generations int       `json:"generations"`
	WWins       int       `json:"wWins"`
	BWins       int       `json:"bWins"`
	SackW       SACKStats `json:"sackW"`
	SackB       SACKStats `json:"sackB"`
	Board       [8][8]*PieceJSON `json:"board"`
	Turn        string    `json:"turn"`
	WChecks     int       `json:"wChecks"`
	BChecks     int       `json:"bChecks"`
	MoveCount   int       `json:"moveCount"`
	WFreedom    float64   `json:"wFreedom"`
	BFreedom    float64   `json:"bFreedom"`
	WCaptured   []string  `json:"wCaptured"`
	BCaptured   []string  `json:"bCaptured"`
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
		SackW:       srv.sackW.stats(),
		SackB:       srv.sackB.stats(),
		Board:       board,
		Turn:        gs.Turn,
		WChecks:     gs.WChecks,
		BChecks:     gs.BChecks,
		MoveCount:   gs.MoveCount,
		WFreedom:    gs.WFreedom,
		BFreedom:    gs.BFreedom,
		WCaptured:   gs.WCaptured,
		BCaptured:   gs.BCaptured,
	}
	srv.mu.Unlock()
	json.NewEncoder(w).Encode(result)
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
		state:    freshGameState(),
		sackW:    newSACK("White"),
		sackB:    newSACK("Black"),
		saveFile: saveFile,
	}
	srv.tryLoad()

	http.HandleFunc("/tick",   srv.handleTick)
	http.HandleFunc("/reset",  srv.handleReset)
	http.HandleFunc("/clear",  srv.handleClear)
	http.HandleFunc("/save",   srv.handleSave)
	http.HandleFunc("/status", srv.handleStatus)

	// Serve static files from ./static/
	http.Handle("/", http.FileServer(http.Dir("./static")))

	log.Printf("SACK CHESS v6 — listening on :%s\n", port)
	log.Printf("Save file: %s\n", saveFile)
	log.Printf("Field size: %d | Coils: %d | Neurons: %d\n", fieldSize(), SpiralCoils, NeuronCount)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}