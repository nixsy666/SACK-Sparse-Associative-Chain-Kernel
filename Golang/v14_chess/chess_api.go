package main

// ============================================================================
// CHESS API — v1
// Client for chess-api.com (Stockfish 18 NNUE REST API).
//
// Used in "vs_stockfish" mode: SACK plays one colour, Stockfish plays the
// other. The API also returns eval + winChance which are displayed in the UI
// and can be used as a supplementary reinforcement signal.
//
// API docs: https://chess-api.com/
// Endpoint: POST https://chess-api.com/v1
//   Body:   { "fen": "...", "depth": 12, "maxThinkingTime": 50 }
//   Return: { "move": "e2e4", "eval": 0.3, "winChance": 52.1, ... }
// ============================================================================

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const (
	ChessAPIURL         = "https://chess-api.com/v1"
	ChessAPITimeout     = 6 * time.Second // allow for slow free-tier responses
	ChessAPIDepth       = 8   // depth 8 ~1800 elo — still much stronger than SACK, faster response
	ChessAPIThinkTimeMs = 25  // ms cap — keeps server-side calc quick
)

// ============================================================================
// REQUEST / RESPONSE TYPES
// ============================================================================

type ChessAPIRequest struct {
	FEN             string `json:"fen"`
	Depth           int    `json:"depth,omitempty"`
	MaxThinkingTime int    `json:"maxThinkingTime,omitempty"`
}

type ChessAPIResponse struct {
	// Core result
	Type      string  `json:"type"`      // "move" | "bestmove" | "info"
	Move      string  `json:"move"`      // UCI format: "e2e4", "e7e8q"
	Eval      float64 `json:"eval"`      // centipawn/100, + = white winning
	WinChance float64 `json:"winChance"` // 0-100, 50 = equal

	// Move detail
	From        string `json:"from"`        // algebraic e.g. "e2"
	To          string `json:"to"`          // algebraic e.g. "e4"
	SAN         string `json:"san"`         // short algebraic e.g. "e4"
	Piece       string `json:"piece"`       // piece letter lowercase
	IsCapture   bool   `json:"isCapture"`
	IsCastling  bool   `json:"isCastling"`
	IsPromotion bool   `json:"isPromotion"`
	Promotion   string `json:"promotion"`   // "q"/"r"/"b"/"n" if promotion

	// Extras
	Centipawns      string   `json:"centipawns"`
	Mate            *int     `json:"mate"`
	Depth           int      `json:"depth"`
	ContinuationArr []string `json:"continuationArr"`
	Text            string   `json:"text"`
	TaskID          string   `json:"taskId"`
}

// ============================================================================
// BOARD → FEN
// Converts SACK's internal [8][8]*Piece board to a FEN string.
//
// Limitations (intentional):
//   - Castling availability omitted ("- -") — SACK doesn't track rights.
//   - En passant square omitted ("-").
//   - Stockfish will play strong positional chess without these; castling /
//     en passant moves from Stockfish are caught by the legal-move validator
//     in doTick and gracefully fall back to SACK.
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

// ============================================================================
// API QUERY
// Posts a FEN to chess-api.com, returns Stockfish's best move + evaluation.
// Blocking call — holds the server lock while waiting (max ChessAPITimeout).
// The client's pendingTick flag means the UI simply waits for the longer tick.
// ============================================================================

func queryChessAPI(fen string) (*ChessAPIResponse, error) {
	reqBody, err := json.Marshal(ChessAPIRequest{
		FEN:             fen,
		Depth:           ChessAPIDepth,
		MaxThinkingTime: ChessAPIThinkTimeMs,
	})
	if err != nil {
		return nil, fmt.Errorf("chess-api marshal: %w", err)
	}

	client := &http.Client{Timeout: ChessAPITimeout}

	resp, err := client.Post(ChessAPIURL, "application/json", bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("chess-api post: %w", err)
	}
	defer resp.Body.Close()

	var raw json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("chess-api read: %w", err)
	}

	// Try single object first (normal REST path)
	var single ChessAPIResponse
	if json.Unmarshal(raw, &single) == nil && single.Move != "" {
		return &single, nil
	}

	// Try array — take the last entry with a move (bestmove comes last)
	var arr []ChessAPIResponse
	if json.Unmarshal(raw, &arr) == nil {
		for i := len(arr) - 1; i >= 0; i-- {
			if arr[i].Move != "" {
				return &arr[i], nil
			}
		}
	}

	return nil, fmt.Errorf("chess-api: no valid move in response")
}
