package main

// ============================================================================
// STOCKFISH LOCAL ENGINE — UCI subprocess interface
//
// Replaces the chess-api.com HTTP integration. Spawns a local Stockfish
// binary and talks the UCI protocol over stdin/stdout pipes. One persistent
// process is shared across all queries (guarded by sfMu).
//
// Binary discovery order:
//   1. STOCKFISH_PATH env var
//   2. ./stockfish.exe  (Windows, project directory)
//   3. ./stockfish      (Linux/Mac, project directory)
//   4. "stockfish" on PATH
//
// UCI flow per query:
//   → position fen <fen>
//   → go movetime <ms>
//   ← info ... score cp N ...   (captured for eval)
//   ← bestmove e2e4 [ponder ...]
// ============================================================================

import (
	"bufio"
	"fmt"
	"math"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	//StockfishMoveTimeMs = 100  // ms per move — fast but still strong
	StockfishMoveTimeMs  = 10   // ms per move — faster easier for sack learning
	StockfishInitTimeMs  = 2000 // ms to wait for uciok / readyok on startup
	StockfishQueryTimeMs = 3000 // ms ceiling per bestmove query
)

// ============================================================================
// RESPONSE TYPE
// Shared with main.go — keeps the same shape as the old REST API response
// so no changes are needed in doTick / handleMode / TickResult.
// ============================================================================

type ChessAPIResponse struct {
	Type        string  `json:"type"`
	Move        string  `json:"move"`      // UCI: "e2e4", "e7e8q"
	Eval        float64 `json:"eval"`      // pawns, + = white winning
	WinChance   float64 `json:"winChance"` // 0-100, 50 = equal
	SAN         string  `json:"san"`       // empty for local engine (main.go falls back to Move)
	From        string  `json:"from"`
	To          string  `json:"to"`
	Piece       string  `json:"piece"`
	IsCapture   bool    `json:"isCapture"`
	IsCastling  bool    `json:"isCastling"`
	IsPromotion bool    `json:"isPromotion"`
	Promotion   string  `json:"promotion"`
	Mate        *int    `json:"mate"`
}

// ============================================================================
// STOCKFISH PROCESS
// ============================================================================

type stockfishProc struct {
	cmd    *exec.Cmd
	writer *bufio.Writer
	lines  chan string // persistent reader goroutine pushes every line here
	mu     sync.Mutex  // guards UCI I/O per query
	ready  bool
}

var (
	sfProc *stockfishProc
	sfMu   sync.Mutex // guards sfProc init retry
)

// findStockfish resolves the Stockfish binary path.
// Accepts any filename starting with "stockfish" so long release names like
// stockfish-windows-x86-64-avx2.exe are matched without renaming.
//
// Search order:
//  1. STOCKFISH_PATH env var
//  2. Directory containing the running executable
//  3. Current working directory
//  4. System PATH
func findStockfish() string {
	if p := os.Getenv("STOCKFISH_PATH"); p != "" {
		fmt.Printf("  [sf] using STOCKFISH_PATH env: %s\n", p)
		return p
	}

	// Dirs to scan: exe dir first, then cwd
	var dirs []string
	if exe, err := os.Executable(); err == nil {
		dirs = append(dirs, strings.ReplaceAll(exe[:strings.LastIndexAny(exe, `/\`)], "\\", "/"))
	}
	if cwd, err := os.Getwd(); err == nil {
		dirs = append(dirs, strings.ReplaceAll(cwd, "\\", "/"))
	}

	fmt.Printf("  [sf] scanning for stockfish binary in: %v\n", dirs)
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() && strings.HasPrefix(strings.ToLower(e.Name()), "stockfish") {
				full := dir + "/" + e.Name()
				fmt.Printf("  [sf] found: %s\n", full)
				return full
			}
		}
	}

	// Fall back to PATH lookup
	if p, err := exec.LookPath("stockfish"); err == nil {
		fmt.Printf("  [sf] found on PATH: %s\n", p)
		return p
	}

	fmt.Println("  [sf] stockfish binary not found in any search location")
	return ""
}

// initStockfish starts the Stockfish process and completes the UCI handshake.
// Called once via sfOnce. If it fails, sfProc.ready stays false and all queries
// fall back to SACK.
func initStockfish() {
	path := findStockfish()
	if path == "" {
		fmt.Println("⚠ Stockfish binary not found — vs_stockfish mode will use SACK fallback")
		fmt.Println("  Place stockfish.exe in the project folder or set STOCKFISH_PATH env var")
		return
	}

	cmd := exec.Command(path)
	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		fmt.Printf("⚠ Stockfish stdin pipe: %v\n", err)
		return
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		fmt.Printf("⚠ Stockfish stdout pipe: %v\n", err)
		return
	}
	if err := cmd.Start(); err != nil {
		fmt.Printf("⚠ Stockfish start failed: %v\n", err)
		return
	}

	sf := &stockfishProc{
		cmd:    cmd,
		writer: bufio.NewWriter(stdinPipe),
		lines:  make(chan string, 256),
	}

	// Single persistent goroutine reads all stdout lines for the lifetime
	// of the process. Queries consume from this channel — no per-query
	// goroutines competing over the same pipe.
	scanner := bufio.NewScanner(stdoutPipe)
	go func() {
		for scanner.Scan() {
			sf.lines <- scanner.Text()
		}
		close(sf.lines)
	}()

	// UCI handshake
	sf.send("uci")
	if !sf.waitFor("uciok", StockfishInitTimeMs) {
		fmt.Println("⚠ Stockfish did not respond with uciok — check binary")
		cmd.Process.Kill()
		return
	}
	sf.send("setoption name Threads value 1")
	sf.send("setoption name Hash value 32")
	sf.send("isready")
	if !sf.waitFor("readyok", StockfishInitTimeMs) {
		fmt.Println("⚠ Stockfish did not respond with readyok")
		cmd.Process.Kill()
		return
	}

	sf.ready = true
	sfProc = sf
	fmt.Printf("✓ Stockfish ready: %s\n", path)
}

// send writes a UCI command to the Stockfish stdin.
// Must be called with sf.mu held (or during single-threaded init).
func (sf *stockfishProc) send(cmd string) {
	sf.writer.WriteString(cmd + "\n")
	sf.writer.Flush()
}

// waitFor drains the lines channel until a line containing target arrives
// or the timeout elapses. Uses the shared persistent channel — no races.
func (sf *stockfishProc) waitFor(target string, timeoutMs int) bool {
	deadline := time.After(time.Duration(timeoutMs) * time.Millisecond)
	for {
		select {
		case line, ok := <-sf.lines:
			if !ok {
				return false // channel closed — process died
			}
			if strings.Contains(line, target) {
				return true
			}
		case <-deadline:
			return false
		}
	}
}

// ============================================================================
// QUERY
// Sends a FEN to the local Stockfish process, returns bestmove + eval.
// Thread-safe: acquires sf.mu for the full command/response cycle.
// ============================================================================

// killAndReset marks sfProc as dead so the next query triggers a restart.
// Called when the pipe closes or a query times out.
func killAndReset() {
	if sfProc != nil {
		if sfProc.cmd != nil && sfProc.cmd.Process != nil {
			sfProc.cmd.Process.Kill()
		}
		sfProc.ready = false
		sfProc = nil
	}
}

func queryChessAPI(fen string) (*ChessAPIResponse, error) {
	// Init/restart — retried on every call so a dead process gets respawned.
	sfMu.Lock()
	if sfProc == nil || !sfProc.ready {
		killAndReset()
		initStockfish()
	}
	sfMu.Unlock()

	if sfProc == nil || !sfProc.ready {
		return nil, fmt.Errorf("stockfish not available")
	}

	sfProc.mu.Lock()
	defer sfProc.mu.Unlock()

	// ucinewgame clears Stockfish's hash between games — prevents stale
	// transposition table entries bleeding across games.
	sfProc.send("ucinewgame")
	sfProc.send("isready")

	// Wait for readyok before sending the position — ensures the hash is
	// fully cleared and no output from a previous search is still in flight.
	if !sfProc.waitFor("readyok", 1000) {
		sfMu.Lock()
		killAndReset()
		sfMu.Unlock()
		return nil, fmt.Errorf("stockfish: readyok timeout — process restarted")
	}

	// Send position + search command
	sfProc.send("position fen " + fen)
	sfProc.send(fmt.Sprintf("go movetime %d", StockfishMoveTimeMs))

	// Read lines from the persistent channel until bestmove arrives.
	bestMove := ""
	lastScoreCp := 0
	isMate := false
	mateIn := 0
	timeout := time.After(time.Duration(StockfishQueryTimeMs) * time.Millisecond)

outer:
	for {
		select {
		case line, ok := <-sfProc.lines:
			if !ok {
				// Persistent reader closed — process died
				sfMu.Lock()
				killAndReset()
				sfMu.Unlock()
				return nil, fmt.Errorf("stockfish pipe closed")
			}
			if strings.HasPrefix(line, "info") {
				parts := strings.Fields(line)
				for i, p := range parts {
					if p == "cp" && i+1 < len(parts) {
						if v, err := strconv.Atoi(parts[i+1]); err == nil {
							lastScoreCp = v
							isMate = false
						}
					}
					if p == "mate" && i+1 < len(parts) {
						if v, err := strconv.Atoi(parts[i+1]); err == nil {
							mateIn = v
							isMate = true
						}
					}
				}
			} else if strings.HasPrefix(line, "bestmove") {
				parts := strings.Fields(line)
				if len(parts) >= 2 && parts[1] != "(none)" {
					bestMove = parts[1]
				}
				break outer
			}
		case <-timeout:
			// Timed out — tell Stockfish to stop, collect the forced bestmove
			sfProc.send("stop")
			select {
			case line := <-sfProc.lines:
				if strings.HasPrefix(line, "bestmove") {
					parts := strings.Fields(line)
					if len(parts) >= 2 && parts[1] != "(none)" {
						bestMove = parts[1]
					}
				}
			case <-time.After(500 * time.Millisecond):
			}
			break outer
		}
	}

	if bestMove == "" {
		return nil, fmt.Errorf("stockfish: no bestmove received")
	}

	// Convert centipawn score to eval (pawns) and win chance (0-100)
	evalPawns := float64(lastScoreCp) / 100.0
	var winChance float64
	if isMate {
		if mateIn > 0 {
			evalPawns = 99.0
			winChance = 99.0
		} else {
			evalPawns = -99.0
			winChance = 1.0
		}
	} else {
		// Logistic win probability from centipawn advantage
		winChance = 100.0 / (1.0 + math.Exp(-evalPawns/2.0))
	}

	return &ChessAPIResponse{
		Type:      "bestmove",
		Move:      bestMove,
		Eval:      evalPawns,
		WinChance: winChance,
	}, nil
}

// ============================================================================
// BOARD → FEN
// Converts SACK's internal [8][8]*Piece board to a FEN string.
//
// Limitations (intentional):
//   - Castling availability omitted ("- -") — SACK doesn't track rights.
//   - En passant square omitted ("-").
//   - Stockfish plays strong positional chess without these; castling /
//     en passant moves are caught by the legal-move validator in doTick
//     and gracefully fall back to SACK.
// ============================================================================

func boardToFEN(board Board, turn string, moveNum int) string {
	var sb strings.Builder
	for row := 0; row < 8; row++ {
		empty := 0
		for col := 0; col < 8; col++ {
			p := board[row][col]
			if p == nil {
				empty++
				continue
			}
			if empty > 0 {
				sb.WriteByte(byte('0' + empty))
				empty = 0
			}
			// White pieces are uppercase in FEN; black pieces lowercase.
			pType := p.Type // already uppercase "K","Q","R","B","N","P"
			if p.Color == "b" {
				pType = strings.ToLower(pType)
			}
			sb.WriteString(pType)
		}
		if empty > 0 {
			sb.WriteByte(byte('0' + empty))
		}
		if row < 7 {
			sb.WriteByte('/')
		}
	}
	fullMove := moveNum/2 + 1
	sb.WriteString(fmt.Sprintf(" %s - - 0 %d", turn, fullMove))
	return sb.String()
}

// ============================================================================
// UCI → Move
// Parses a UCI move string into SACK's Move struct.
//
// UCI format: "<fromFile><fromRank><toFile><toRank>[promotionPiece]"
//   e.g.  "e2e4"   normal move
//         "e7e8q"  pawn promotion to queen
//
// Board coordinate mapping:
//   UCI file:  'a'=0 … 'h'=7  → board col
//   UCI rank:  '1'=row7 … '8'=row0  (SACK stores rank-8 at row-0)
// ============================================================================

func uciToMove(uci string) (Move, string, bool) {
	uci = strings.TrimSpace(strings.ToLower(uci))
	if len(uci) < 4 {
		return Move{}, "", false
	}

	fromFile := int(uci[0] - 'a')
	fromRankIdx := int(uci[1] - '1') // 0-based: '1'→0, '8'→7
	toFile := int(uci[2] - 'a')
	toRankIdx := int(uci[3] - '1')

	if fromFile < 0 || fromFile > 7 || fromRankIdx < 0 || fromRankIdx > 7 ||
		toFile < 0 || toFile > 7 || toRankIdx < 0 || toRankIdx > 7 {
		return Move{}, "", false
	}

	// SACK rows: row 0 = rank 8, row 7 = rank 1
	fromRow := 7 - fromRankIdx
	toRow := 7 - toRankIdx

	promo := ""
	if len(uci) >= 5 {
		promo = string(uci[4]) // "q", "r", "b", "n"
	}

	return Move{From: [2]int{fromRow, fromFile}, To: [2]int{toRow, toFile}}, promo, true
}
