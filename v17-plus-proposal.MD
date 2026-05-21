# SACK Chess — Proposed Changes Summary for v-17+
### Session: 21 May 2026 | Nixsy × Claude

---

## Core Philosophy Shift

Every previous version injected adult chess knowledge before the tomb had a chance to discover it. Piece weights, threat tables, king safety heuristics — these were answers handed to a system that should have been finding them itself. The Gemini flowmap made this visible. The fix is to remove all of it and let the tomb be the only source of positional understanding.

**Two five year olds. A book of legal moves. Nothing else.**

---

## What Gets Removed

These functions stay in `board.go` for legal move generation but are **completely removed from scoring**:

- `pieceValue` — out of scoring entirely
- `pieceThreat` — out of scoring entirely  
- `kingSafety` — removed
- `gravityDanger` — removed
- `threatProximity` — removed
- `kingFreedom` — removed
- `phaseKingSafetyWeight` — removed
- `sortMovesByTactics` — removed
- `reinforceCheck` / `reinforceCheckGradient` — removed
- `reinforceCapture` — removed
- Lookahead heuristic cascade — removed

`scoreMove` becomes: **tomb peek confidence + randomness only.**

---

## What Replaces Them

### Learning Signals — Simple and Honest

| Event | Signal |
|---|---|
| Piece captured (mid-game) | Positive pulse to active piece tomb — chain included a capture, game continued |
| Piece lost | Stored in adversarial tomb — shape of what went wrong |
| Game won | Reinforce full causal chain |
| Game lost | Store opponent's final N moves as adversarial pattern — no punishment cascade |

No injected weights. No programmed opinions. The tomb earns every opinion it develops.

---

## Proposed Structural Additions

### 1. Board-Agnostic Chain Encoding
Chain links currently encode absolute spiral positions. New encoding expresses **events and relationships** — piece type, action type, relative proximity change, mobility delta before/after. Same narrative, any board position.

A knight fork on d5 and a knight fork on b3 become the **same chain shape**. The tomb recognises the class of situation, not the specific instance. Chains get longer and more transferable.

### 2. Piece-Relative Pressure Snapshot
`pressureSnapshot` is currently king-centric. Add `piecePressureSnapshot(b, pr, pf, color)` — each piece encodes its own neighbourhood geometry. The rook sees the board from the rook's position. The pawn sees it from the pawn's position.

### 3. Adversarial Tomb
New `AdversarialTomb` field on `DelegatorSack`. Fed from opponent's last N moves on loss. Future play queries this tomb — high recognition confidence means a known losing pattern is forming. The system learns *watch for this* rather than *don't do that*.

### 4. Unified Memory Blob
All tombs (piece children, shared, adversarial) unified into a single blob per side. Loaded warm into RAM at boot. Written every N games rather than after every win. Single consistent snapshot — no more mismatched timestamps between piece tomb and shared tomb.

Benefits:
- Internally consistent state at all times
- Single fingerprint validates everything
- Dramatically reduced disk I/O
- Opens the path to unified memory object where piece children are regions of the same field rather than separate objects communicating through interfaces

---

## The Emergence Test

Run overnight. Cold start. Genuinely naive.

**If natural causality appears** — coherent sequences where early piece activity leads to mid-game advantage leads to winning patterns without programming — the principle is proven. Everything downstream (language geometry, LLM plugin, FPGA hardware path) rests on this.

**If it doesn't appear** — the rethink happens at the right level, in chess, before committing the architecture further.

Either outcome is valuable. Either way we know something real.

---

## Why This Matters Beyond Chess

The board-agnostic chain encoding is the same architecture required for:

- **Language geometry** — vector paths replacing words, FAISS handling nearest-neighbour lookup, SACK handling causal chain memory across those vectors
- **LLM plugin** — SACK as living memory layer alongside Claude, history that feels like memory rather than a logfile
- **Contextual FAISS** — vector chains with directional causal relationships, `[do before] → [cooking] → [implementation] → [raise temperature]` emerging naturally from tomb density
- **FPGA implementation** — unified blob maps directly to hardware memory layout, 12-layer tomb pipeline becomes 12 physical pipeline stages, sub-microsecond pulse latency
- **Custom silicon** — same architecture etched permanently, fraction of FPGA power consumption, rack-scale deployment

Chess proves the principle in a closed formal system where everything is measurable. Language needs the same spine with additional steps — FAISS for vector lookup, coherence gating replacing legal move validation, demonstrated intent replacing the game's terminal signal.

---

## The Queen Problem — Solved Correctly

Previous versions: `pieceValue["Q"] = 8` — adult knowledge injected at birth.

New version: The queen's freedom of movement produces more chains, more varied neighbourhood geometries, denser tomb trails across more game situations. By game 500 the tomb knows the queen is valuable because she appears *everywhere* in winning chains. The pawn sitting behind the queen learns her value through felt absence when she's gone — not through a protection weight.

**That's not a weight. That's understanding.**

---

## Natural Strategy Emergence

Consecutive captures leading to a win produce a rich, dense, deeply linked chain. A lucky one-blunder win produces a sparse thin chain. Aggressive coordinated play naturally outweighs passive lucky play in tomb density terms. The system doesn't learn "control the centre" as a rule — it learns that certain opening chain shapes kept leading to positions where captures were available and wins followed.

**Strategy as crystallised experience. Not programmed principle.**

---

*SACK — Sparse Associative Chain Kernel*  
*SOSL v3.0 — Nixsy (Nicholas Laidlaw)*  
*ai-lab.host | @NixPix_2025*