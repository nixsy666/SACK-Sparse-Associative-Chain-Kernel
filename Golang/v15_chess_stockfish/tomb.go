package main

// ============================================================================
// TOMB — v13
// Wedge-shaped memory structure for SACK.
//
// The tomb is a wedge that widens from top (narrow entry) to bottom (deep memory).
// Neurons stand perpendicular to the spiral surface — like bristles on a brush.
// New input pulses enter at the narrow top and travel downward, collecting
// partial resonances from existing chains as they go.
//
// Depth = importance + frequency. Chains that get reused are pushed deeper.
// Chains that go unused stay shallow and get overwritten by new input.
//
// Layer 0 (top/narrow):  TombBaseWidth slots  — fresh input, fast, cheap
// Layer N (bottom/wide): up to TombMaxWidth    — deep memory, dense, slow
//
// The two cortices (semantic + logic) share the same tomb geometry but
// maintain separate chain stores — they are two spirals inside one wedge.
// ============================================================================

import (
//	"encoding/json"
//	"fmt"
	"math"
//	"os"
)

const (
	TombDepths    = 12   // number of layers in the wedge
	TombBaseWidth = 300  // layer 0 width (narrow top)
	TombMaxWidth  = 30000 // layer 11 width (wide bottom)
	TombGrowthExp = 1.42 // exponential growth factor per layer (approx 100x over 12 layers)

	// Pulse parameters
	PulseFullMatchThreshold    = 0.85 // resonance score above which a chain is "fully matched"
	PulsePartialMatchThreshold = 0.45 // resonance score for partial / fragment match
	PulseMaxCandidatesPerLayer = 16   // max chains to score per layer during a pulse

	// Promotion thresholds — how many reuses before a chain earns deeper placement
	PromoteReusesBase = 3  // layer 0→1 needs 3 reuses
	PromoteReusesGain = 2  // each layer deeper needs 2 more reuses
)

// ============================================================================
// NORMAL VECTOR
// Each neuron stands perpendicular to the spiral skin.
// Its orientation is derived purely from its position on the spiral —
// the outward normal at that point on the Archimedean spiral.
// ============================================================================

// spiralNormal returns the outward normal angle (radians) for a given spiral
// position. For an Archimedean spiral r = a + b*theta, the tangent angle at
// angle theta is atan(r / b). The normal is tangent + pi/2.
func spiralNormal(spiralPos int) float64 {
	fs := fieldSize()
	if fs == 0 {
		return 0
	}
	coil := spiralCoil(spiralPos)
	// Map position to angle in [0, 2*pi*SpiralCoils]
	_ = float64(spiralPos) / float64(fs) * 2 * math.Pi * SpiralCoils
	// r grows linearly with coil; b is the spacing between coils
	b := 1.0
	r := float64(SpiralBase) + float64(coil)*b*2*math.Pi
	// Tangent angle for Archimedean spiral
	tangent := math.Atan2(r, b)
	// Normal = tangent + pi/2 (outward)
	return tangent + math.Pi/2
}

// ============================================================================
// TOMB CHAIN
// A stored chain inside the tomb — the unit of memory.
// ============================================================================

type TombChain struct {
	// Core identity
	ChainSig  uint64    // rolling hash of the neuron sequence that produced this chain
	Links     []ChainLink // the actual neuron sequence

	// Resonance fingerprint — composite of subsack signals at chain formation
	// Used for fast partial matching without replaying the full chain
	Resonance uint64

	// Pressure snapshot at time of storage — the board state that produced this
	Pressure  [CoarseCells]float32

	// Memory depth management
	TombDepth  int // which layer this chain lives at (0=shallow, 11=deep)
	ReuseCount int // how many times this chain was partially or fully matched
	Age        int // generations since last match (for eviction)

	// Match history — partial match contributions
	// Tracks which fragments of this chain resonated with recent pulses
	FragmentHits int
}

// promoteThreshold returns how many reuses this chain needs to go one layer deeper
func (tc *TombChain) promoteThreshold() int {
	return PromoteReusesBase + tc.TombDepth*PromoteReusesGain
}

// eligible returns true if this chain has earned promotion to the next layer
func (tc *TombChain) eligible() bool {
	return tc.ReuseCount >= tc.promoteThreshold() && tc.TombDepth < TombDepths-1
}

// ============================================================================
// TOMB LAYER
// A single horizontal slice of the wedge.
// ============================================================================

type TombLayer struct {
	Depth    int
	Width    int // max chains this layer can hold
	Chains   []*TombChain
}

func newTombLayer(depth int) *TombLayer {
	width := tombLayerWidth(depth)
	return &TombLayer{
		Depth:  depth,
		Width:  width,
		Chains: make([]*TombChain, 0, min(width, 256)),
	}
}

// tombLayerWidth computes the width of a layer using exponential growth
func tombLayerWidth(depth int) int {
	w := float64(TombBaseWidth) * math.Pow(TombGrowthExp, float64(depth))
	if w > TombMaxWidth {
		return TombMaxWidth
	}
	return int(w)
}

// store adds a chain to this layer. If full, evicts the oldest/least-used chain.
func (tl *TombLayer) store(tc *TombChain) {
	tc.TombDepth = tl.Depth
	if len(tl.Chains) < tl.Width {
		tl.Chains = append(tl.Chains, tc)
		return
	}
	// Evict: find chain with highest age and lowest reuse
	evictIdx := 0
	worstScore := -1
	for i, c := range tl.Chains {
		score := c.ReuseCount - c.Age*2
		if worstScore == -1 || score < worstScore {
			worstScore = score
			evictIdx = i
		}
	}
	tl.Chains[evictIdx] = tc
}

// ============================================================================
// PULSE RESULT
// What comes back from a tomb pulse — the memory response to new input.
// ============================================================================

type PulseMatch struct {
	Chain     *TombChain
	Score     float64
	IsFull    bool   // score >= PulseFullMatchThreshold
	IsPartial bool   // score >= PulsePartialMatchThreshold
	Depth     int    // which layer the match was found at
}

type PulseResult struct {
	FullMatches    []PulseMatch
	PartialMatches []PulseMatch
	TopScore       float64
	TopDepth       int    // deepest layer that had a match
	// Composite resonance assembled from partial matches
	// This biases the SACK engine's next move selection
	CompositeResonance uint64
	// Confidence modifier: 0..1 — how much of the input was "recognised"
	RecognitionConf float64
}

// ============================================================================
// TOMB
// The full wedge structure — two cortices (semantic + logic) share one tomb.
// ============================================================================

type Tomb struct {
	Layers [TombDepths]*TombLayer

	// Running stats
	TotalStored   int
	TotalPulses   int
	TotalPromoted int
}

func newTomb() *Tomb {
	t := &Tomb{}
	for d := 0; d < TombDepths; d++ {
		t.Layers[d] = newTombLayer(d)
	}
	return t
}

// ============================================================================
// PULSE — the core operation
// A new input signal enters at the narrow top and travels downward.
// At each layer it scores resonance against stored chains.
// Full and partial matches are collected.
// Matched chains get ReuseCount incremented → eligible for depth promotion.
// ============================================================================

func (t *Tomb) Pulse(inputSig uint64, pressure [CoarseCells]float32) PulseResult {
	t.TotalPulses++
	result := PulseResult{}

	var compositeRes uint64
	var totalScore float64
	var matchCount int

	for d := 0; d < TombDepths; d++ {
		layer := t.Layers[d]
		if len(layer.Chains) == 0 {
			continue
		}

		// Score candidates — limit per layer to keep pulses cheap
		candidates := layer.Chains
		if len(candidates) > PulseMaxCandidatesPerLayer {
			// Sample from the layer — prefer recent chains at shallow layers,
			// well-used chains at deep layers
			candidates = t.sampleLayer(layer, PulseMaxCandidatesPerLayer)
		}

		for _, tc := range candidates {
			score := t.matchScore(inputSig, pressure, tc)
			if score <= 0 {
				continue
			}

			match := PulseMatch{
				Chain: tc,
				Score: score,
				Depth: d,
			}

			if score >= PulseFullMatchThreshold {
				match.IsFull = true
				result.FullMatches = append(result.FullMatches, match)
				tc.ReuseCount++
				tc.Age = 0
			} else if score >= PulsePartialMatchThreshold {
				match.IsPartial = true
				result.PartialMatches = append(result.PartialMatches, match)
				tc.FragmentHits++
				tc.ReuseCount++
				tc.Age = 0
			}

			if score > result.TopScore {
				result.TopScore = score
				result.TopDepth = d
			}

			// Weave this chain's resonance into the composite
			compositeRes ^= tc.Resonance * uint64(score*1000)
			totalScore += score
			matchCount++
		}

		// Age all unmatched chains in this layer
		for _, tc := range layer.Chains {
			tc.Age++
		}
	}

	result.CompositeResonance = compositeRes

	// Recognition confidence: how well did the tomb "know" this input
	if matchCount > 0 {
		result.RecognitionConf = math.Min(1.0, totalScore/float64(matchCount))
	}

	// After pulse, promote eligible chains to deeper layers
	t.promoteEligible()

	return result
}

// matchScore computes resonance between incoming signal and a stored chain.
// Two components:
//   1. Signal similarity — XOR distance between input sig and chain sig
//   2. Pressure similarity — cosine-like similarity between pressure snapshots
func (t *Tomb) matchScore(inputSig uint64, pressure [CoarseCells]float32, tc *TombChain) float64 {
	// Signal component — popcount of matching bits
	xor := inputSig ^ tc.Resonance
	setBits := 0
	x := xor
	for x != 0 {
		setBits += int(x & 1)
		x >>= 1
	}
	sigScore := 1.0 - float64(setBits)/64.0

	// Pressure component — dot product normalised
	var dot, magA, magB float64
	for i := 0; i < CoarseCells; i++ {
		a := float64(pressure[i])
		b := float64(tc.Pressure[i])
		dot += a * b
		magA += a * a
		magB += b * b
	}
	var pressScore float64
	if magA > 0 && magB > 0 {
		pressScore = dot / (math.Sqrt(magA) * math.Sqrt(magB))
		// Normalise from [-1,1] to [0,1]
		pressScore = (pressScore + 1) / 2
	}

	// Weighted combination — signal is the primary signal, pressure is context
	return sigScore*0.6 + pressScore*0.4
}

// sampleLayer picks up to n chains from a layer.
// At shallow layers, prefers recently stored (low age).
// At deep layers, prefers well-used (high reuse).
func (t *Tomb) sampleLayer(layer *TombLayer, n int) []*TombChain {
	if len(layer.Chains) <= n {
		return layer.Chains
	}
	// Simple: take every Kth chain to spread the sample
	step := len(layer.Chains) / n
	if step < 1 {
		step = 1
	}
	result := make([]*TombChain, 0, n)
	for i := 0; i < len(layer.Chains) && len(result) < n; i += step {
		result = append(result, layer.Chains[i])
	}
	return result
}

// promoteEligible scans all layers and moves eligible chains one level deeper.
func (t *Tomb) promoteEligible() {
	for d := TombDepths - 2; d >= 0; d-- {
		layer := t.Layers[d]
		var remaining []*TombChain
		for _, tc := range layer.Chains {
			if tc.eligible() {
				tc.TombDepth = d + 1
				tc.ReuseCount = 0 // reset — needs to earn the next promotion too
				t.Layers[d+1].store(tc)
				t.TotalPromoted++
			} else {
				remaining = append(remaining, tc)
			}
		}
		layer.Chains = remaining
	}
}

// Store deposits a new chain into the tomb at layer 0 (the narrow entry).
// Called after a SACK chain is completed and reinforced.
func (t *Tomb) Store(links []ChainLink, chainSig uint64, resonance uint64, pressure [CoarseCells]float32) *TombChain {
	tc := &TombChain{
		ChainSig:  chainSig,
		Links:     make([]ChainLink, len(links)),
		Resonance: resonance,
		Pressure:  pressure,
		TombDepth: 0,
	}
	copy(tc.Links, links)
	t.Layers[0].store(tc)
	t.TotalStored++
	return tc
}

// ============================================================================
// PEEK MATCH — read-only resonance query (no side effects)
// Used during move scoring so hypothetical evaluations don't age chains,
// promote entries, or inflate TotalPulses.
// Returns RecognitionConf in 0..1 — same semantics as Pulse.RecognitionConf.
// ============================================================================

func (t *Tomb) PeekMatch(inputSig uint64, pressure [CoarseCells]float32) float64 {
	var totalScore float64
	var matchCount int
	for d := 0; d < TombDepths; d++ {
		layer := t.Layers[d]
		if len(layer.Chains) == 0 {
			continue
		}
		candidates := layer.Chains
		if len(candidates) > PulseMaxCandidatesPerLayer {
			candidates = t.sampleLayer(layer, PulseMaxCandidatesPerLayer)
		}
		for _, tc := range candidates {
			score := t.matchScore(inputSig, pressure, tc)
			if score >= PulsePartialMatchThreshold {
				totalScore += score
				matchCount++
			}
		}
	}
	if matchCount > 0 {
		return math.Min(1.0, totalScore/float64(matchCount))
	}
	return 0
}

// ============================================================================
// TOMB STATS — for serialisation and monitoring
// ============================================================================

type TombLayerStats struct {
	Depth      int `json:"depth"`
	Width      int `json:"width"`
	Stored     int `json:"stored"`
	AvgReuse   float64 `json:"avgReuse"`
}

type TombStats struct {
	Layers        []TombLayerStats `json:"layers"`
	TotalStored   int              `json:"totalStored"`
	TotalPulses   int              `json:"totalPulses"`
	TotalPromoted int              `json:"totalPromoted"`
}

func (t *Tomb) Stats() TombStats {
	stats := TombStats{
		TotalStored:   t.TotalStored,
		TotalPulses:   t.TotalPulses,
		TotalPromoted: t.TotalPromoted,
	}
	for d := 0; d < TombDepths; d++ {
		layer := t.Layers[d]
		var totalReuse int
		for _, tc := range layer.Chains {
			totalReuse += tc.ReuseCount
		}
		avgReuse := 0.0
		if len(layer.Chains) > 0 {
			avgReuse = float64(totalReuse) / float64(len(layer.Chains))
		}
		stats.Layers = append(stats.Layers, TombLayerStats{
			Depth:    d,
			Width:    layer.Width,
			Stored:   len(layer.Chains),
			AvgReuse: avgReuse,
		})
	}
	return stats
}