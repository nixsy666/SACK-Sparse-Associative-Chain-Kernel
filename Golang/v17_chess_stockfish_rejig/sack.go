package main

import (
	"fmt"
	"math"
	"math/rand"
)

// ============================================================================
// SACK — v13
// Spiral Associative Cortex Kernel + Tomb memory wedge.
//
// The tomb is a wedge-shaped memory structure sitting beneath the spiral cortex.
// Neurons stand perpendicular to the spiral surface (90° to the skin).
// New inputs pulse through the tomb narrow→wide, harvesting partial resonances
// from existing chains. Chains proven by reuse are pushed deeper.
// Chains never reused stay shallow and get overwritten.
// ============================================================================

const (
	NeuronCount  = 64
	ChainMax     = 64
	ClusterMax   = 52
	CoarseCells  = 16
	SpiralCoils  = 8
	SpiralBase   = 8
	SubSackCount = 32 // subsacks per neuron
)

// Growth states as bit depth — the doubling ladder
const (
	GrowthDormant      = 4
	GrowthWaking       = 8
	GrowthActive       = 16
	GrowthSpecialising = 32
	GrowthExpert       = 64
)

var growthStages = []int{
	GrowthDormant,
	GrowthWaking,
	GrowthActive,
	GrowthSpecialising,
	GrowthExpert,
}

// growthTrigger — how many times the same chain sig must recur to promote
// Scales with stage: harder to reach expert than to wake from dormant
func growthTrigger(bits int) int {
	switch bits {
	case GrowthDormant:      return 4
	case GrowthWaking:       return 8
	case GrowthActive:       return 16
	case GrowthSpecialising: return 32
	}
	return 64
}

// ============================================================================
// SPIRAL MAP
// ============================================================================
type SpiralMap struct {
	Sizes  [SpiralCoils]int
	Starts [SpiralCoils]int
	Total  int
}

var spiralMap SpiralMap

func initSpiralMap() {
	total := 0
	for k := 0; k < SpiralCoils; k++ {
		size := SpiralBase * (1 << uint(k))
		spiralMap.Sizes[k] = size
		spiralMap.Starts[k] = total
		total += size
	}
	spiralMap.Total = total
}

func fieldSize() int { return spiralMap.Total }

func spiralCoil(pos int) int {
	fs := fieldSize()
	p := ((pos % fs) + fs) % fs
	for k := SpiralCoils - 1; k >= 0; k-- {
		if p >= spiralMap.Starts[k] {
			return k
		}
	}
	return 0
}

func isCoilBoundary(pos int) bool {
	fs := fieldSize()
	p := ((pos % fs) + fs) % fs
	for k := 0; k < SpiralCoils; k++ {
		s := spiralMap.Starts[k]
		e := s + spiralMap.Sizes[k] - 1
		if p == s || p == e {
			return true
		}
	}
	return false
}

func coilRadius(pos int) int { return 4 + spiralCoil(pos)*3 }

// ============================================================================
// PRESSURE SNAPSHOT
// ============================================================================
func pressureSnapshot(b Board, color string) [CoarseCells]float32 {
	var bins [CoarseCells]float32
	kr, kf, ok := getKingPos(b, color)
	if !ok {
		return bins
	}
	for r := 0; r < 8; r++ {
		for f := 0; f < 8; f++ {
			p := b[r][f]
			if p == nil || (r == kr && f == kf) {
				continue
			}
			angle := math.Atan2(float64(r-kr), float64(f-kf))
			angleDeg := math.Mod(angle*180/math.Pi+180, 180)
			cell := int(angleDeg/180.0*CoarseCells) % CoarseCells
			dist := math.Hypot(float64(f-kf), float64(r-kr))
			weight := float32((pieceValue[p.Type]) / (dist + 1))
			if p.Color == color {
				bins[cell] += weight
			} else {
				bins[cell] -= weight
			}
		}
	}
	var maxV float32
	for i := 0; i < CoarseCells; i++ {
		v := bins[i]
		if v < 0 {
			v = -v
		}
		if v > maxV {
			maxV = v
		}
	}
	if maxV > 0 {
		for i := 0; i < CoarseCells; i++ {
			bins[i] /= maxV
		}
	}
	return bins
}

// moveContextSpiralPos — board-agnostic chain position encoding.
//
// Maps a move to a spiral position using only:
//   - Piece type (which region of the spiral this piece family owns)
//   - Mobility delta (how many squares the piece gains/loses by moving)
//   - Tactical flags: capture and check push toward deeper, denser coils
//
// The same knight fork on d5 and the same knight fork on b3 produce
// identical spiral positions — the tomb learns the CLASS of situation,
// not the specific board instance. This is the key property that allows
// chains from one game to transfer to geometrically similar situations
// in later games without relearning from scratch.
func moveContextSpiralPos(b Board, color string, move Move, isCapture, isCheck bool) int {
	pieceType := "P"
	mobilityBefore := 0
	if p := b[move.From[0]][move.From[1]]; p != nil {
		pieceType = p.Type
		mobilityBefore = len(getPieceMoves(b, move.From[0], move.From[1], false))
	}

	nb := applyMove(b, move)
	mobilityAfter := len(getPieceMoves(nb, move.To[0], move.To[1], false))

	pieceIdx := pieceChildIndex(pieceType)
	if pieceIdx < 0 {
		pieceIdx = 5
	}

	// Mobility delta normalised to [0,1].
	// Typical range -15 to +20; we use -15/+20 as soft bounds.
	mobilityDelta := mobilityAfter - mobilityBefore
	mobNorm := (float64(mobilityDelta) + 15) / 35.0
	if mobNorm < 0 {
		mobNorm = 0
	}
	if mobNorm > 1 {
		mobNorm = 1
	}

	// Tactical coil offset — captures and checks push into denser coils
	// where well-proven chains live. This biases the system toward
	// storing tactical moments at higher resolution without programming
	// in any opinion about their value.
	tacticOffset := 0
	if isCapture {
		tacticOffset += 2
	}
	if isCheck {
		tacticOffset += 2
	}

	// Each of the 6 piece types owns ~1.3 coils in the 8-coil spiral.
	// pieceIdx 0=K 1=Q 2=R 3=B 4=N 5=P
	baseCoil := pieceIdx * SpiralCoils / 6
	targetCoil := baseCoil + tacticOffset
	if targetCoil >= SpiralCoils {
		targetCoil = SpiralCoils - 1
	}

	start := spiralMap.Starts[targetCoil]
	size := spiralMap.Sizes[targetCoil]
	posInCoil := int(mobNorm * float64(size-1))

	pos := start + posInCoil
	if isCoilBoundary(pos) {
		c := spiralCoil(pos)
		cs := spiralMap.Starts[c]
		cz := spiralMap.Sizes[c]
		pos = cs + (cz - 1 - (pos - cs))
	}

	fs := fieldSize()
	return ((pos % fs) + fs) % fs
}

// pressuresToResonance encodes a sequence of pressure snapshots into a
// single uint64 resonance fingerprint. Used to build the adversarial tomb
// key from the opponent's recent board states.
func pressuresToResonance(pressures [][CoarseCells]float32) uint64 {
	var combined uint64
	for i, p := range pressures {
		var enc uint64
		for j := 0; j < CoarseCells; j++ {
			bits := uint64(math.Abs(float64(p[j]))*255) & 0xFF
			enc ^= bits << uint((j*4+i*7)%64)
		}
		enc ^= enc >> 33
		enc *= 0xff51afd7ed558ccd
		enc ^= enc >> 33
		combined ^= enc
	}
	return combined
}

// ============================================================================
// CHAIN SIGNATURE
// Rolling hash of the NID sequence that produced the current moment.
// Each neuron that fires folds its ID into the chain signature.
// Two activations share a chain sig only if they arrived via the same journey.
// ============================================================================

// chainSigStep advances a rolling chain signature by one neuron ID.
// Uses a Fibonacci/golden-ratio hash for good avalanche distribution.
func chainSigStep(sig uint64, nid int) uint64 {
	sig ^= uint64(nid+1) * 0x9e3779b97f4a7c15
	sig ^= sig >> 33
	sig *= 0xff51afd7ed558ccd
	sig ^= sig >> 33
	return sig
}

// ============================================================================
// SUBSACK
// ============================================================================
type SubSack struct {
	Signal     uint64 // resonance signature at current bit depth
	GrowthBits int    // current bit depth: 4,8,16,32,64
	Delta      uint64 // change between last two activations
	PrevSignal uint64 // previous signal for delta

	// Chain-driven growth tracking
	// Maps chain signature → hit count for that exact causal sequence
	// Capped at top-N entries to avoid unbounded memory growth
	ChainHits map[uint64]int
	TopChain  uint64 // chain sig with most hits at current stage
	TopHits   int    // hit count for TopChain
}

func newSubSack() SubSack {
	return SubSack{
		GrowthBits: GrowthDormant,
		ChainHits:  make(map[uint64]int),
	}
}

// bitMask returns a mask for the current growth bit depth
func (ss *SubSack) bitMask() uint64 {
	if ss.GrowthBits >= 64 {
		return ^uint64(0)
	}
	return (1 << uint(ss.GrowthBits)) - 1
}

// activate fires the subsack, generating a unique signal per subsack index
// via a golden-ratio phase offset — each subsack sees a genuinely different
// slice of the same input, so they diverge and promote independently.
func (ss *SubSack) activate(fieldPos int, pressure [CoarseCells]float32, idx int) {
	sliceStart := (idx * CoarseCells / SubSackCount) % CoarseCells
	var raw uint64
	for i := 0; i < 4; i++ {
		cell := (sliceStart + i) % CoarseCells
		bits := uint64(math.Abs(float64(pressure[cell])) * 255)
		raw |= bits << uint(i*8)
	}
	// Per-subsack phase rotation — breaks block-promotion
	raw ^= uint64(fieldPos*(idx+1)) * 0x9e3779b97f4a7c15
	raw ^= raw >> 33
	raw *= 0xff51afd7ed558ccd
	raw ^= raw >> 33

	masked := raw & ss.bitMask()
	ss.Delta = masked ^ (ss.PrevSignal & ss.bitMask())
	ss.PrevSignal = ss.Signal
	ss.Signal = masked
}

// recordChain records that this subsack fired as part of the given chain sequence.
// If the same chain sig has now fired enough times, promote to next bit depth.
// Returns true if growth occurred.
func (ss *SubSack) recordChain(chainSig uint64) bool {
	if ss.GrowthBits >= GrowthExpert {
		return false
	}

	ss.ChainHits[chainSig]++
	hits := ss.ChainHits[chainSig]

	// Track the dominant chain
	if hits > ss.TopHits {
		ss.TopHits = hits
		ss.TopChain = chainSig
	}

	// Promote if dominant chain has recurred enough times
	if ss.TopHits >= growthTrigger(ss.GrowthBits) {
		ss.grow()
		return true
	}

	// Prune map if it gets large — keep only top 32 chains
	if len(ss.ChainHits) > 32 {
		ss.pruneChains()
	}
	return false
}

// pruneChains keeps only the top 16 chain signatures by hit count
func (ss *SubSack) pruneChains() {
	if len(ss.ChainHits) <= 16 {
		return
	}
	// Find median hit count and drop below it
	var counts []int
	for _, v := range ss.ChainHits {
		counts = append(counts, v)
	}
	// Simple threshold: keep entries with hits > 1
	for k, v := range ss.ChainHits {
		if v <= 1 && k != ss.TopChain {
			delete(ss.ChainHits, k)
		}
	}
}

// grow advances to the next bit depth stage, resetting chain tracking
func (ss *SubSack) grow() {
	for i, stage := range growthStages {
		if stage == ss.GrowthBits && i+1 < len(growthStages) {
			ss.GrowthBits = growthStages[i+1]
			// Reset chain tracking — new resolution, new journey needed
			ss.ChainHits = make(map[uint64]int)
			ss.TopChain = 0
			ss.TopHits = 0
			return
		}
	}
}

// fuzzyMatch returns similarity 0..1 at current bit depth
func (ss *SubSack) fuzzyMatch(ref uint64) float64 {
	mask := ss.bitMask()
	a := ss.Signal & mask
	b := ref & mask
	xor := a ^ b
	matching := ss.GrowthBits - popcount(xor, ss.GrowthBits)
	return float64(matching) / float64(ss.GrowthBits)
}

// popcount counts set bits up to maxBits
func popcount(x uint64, maxBits int) int {
	count := 0
	for i := 0; i < maxBits; i++ {
		if x&(1<<uint(i)) != 0 {
			count++
		}
	}
	return count
}

// ============================================================================
// NEURON
// ============================================================================
type ChainLink struct {
	NID      int
	FieldPos int
	Delta    float64
	Coil     int
	Boundary bool
}

type Neuron struct {
	NID         int
	Field       []float32
	Activations int
	Successes   float64
	Failures    float64
	Clusters    [][]ChainLink
	CoilHits    map[int]int
	PeakPos     int
	IsBoundary  bool
	// Subsack layer
	SubSacks  []SubSack
	Resonance uint64 // composite signature across all subsacks
}

func newNeuron(nid int) *Neuron {
	fs := fieldSize()
	n := &Neuron{
		NID:       nid,
		Field:     make([]float32, fs),
		Successes: 1,
		CoilHits:  make(map[int]int),
	}
	n.SubSacks = make([]SubSack, SubSackCount)
	for i := range n.SubSacks {
		n.SubSacks[i] = newSubSack()
	}
	coilIndex := int(float64(nid) / NeuronCount * SpiralCoils)
	if coilIndex >= SpiralCoils {
		coilIndex = SpiralCoils - 1
	}
	start := spiralMap.Starts[coilIndex]
	size := spiralMap.Sizes[coilIndex]
	peak := start + rand.Intn(size)
	n.Field[peak] = 1.0
	r := coilRadius(peak)
	for i := 1; i <= r; i++ {
		w := float32(1.0 - float64(i)/float64(r+1))
		n.Field[(peak+i)%fs] = w
		n.Field[(peak-i+fs)%fs] = w
	}
	n.PeakPos = peak
	n.CoilHits[coilIndex] = 1
	n.IsBoundary = isCoilBoundary(peak)
	return n
}

// activateSubSacks fires all subsacks and records the chain signature.
// Each subsack independently decides whether to grow based on chain recurrence.
func (n *Neuron) activateSubSacks(fieldPos int, pressure [CoarseCells]float32, chainSig uint64) {
	var composite uint64
	for i := range n.SubSacks {
		ss := &n.SubSacks[i]
		ss.activate(fieldPos, pressure, i)
		ss.recordChain(chainSig)
		composite ^= ss.Signal << uint(i%64)
	}
	n.Resonance = composite
}

// resonanceMatch returns how closely this neuron's resonance matches a reference
func (n *Neuron) resonanceMatch(ref uint64) float64 {
	var total float64
	for i := range n.SubSacks {
		total += n.SubSacks[i].fuzzyMatch(ref >> uint(i%64))
	}
	return total / float64(len(n.SubSacks))
}

// growthProfile returns a summary of subsack maturity across this neuron
func (n *Neuron) growthProfile() map[int]int {
	profile := make(map[int]int)
	for _, ss := range n.SubSacks {
		profile[ss.GrowthBits]++
	}
	return profile
}

func (n *Neuron) resonate(fieldPos int) float64 {
	raw := float64(n.Field[fieldPos])
	total := n.Successes + n.Failures + 0.001
	return raw * (n.Successes / total)
}

func (n *Neuron) bounce(fieldPos int, strength float64) {
	fs := fieldSize()
	r := coilRadius(fieldPos)
	lift := float32(0.06 * math.Min(2.0, strength))
	boundary := isCoilBoundary(fieldPos)
	for i := 0; i < r; i++ {
		w := float32(1.0 - float64(i)/float64(r))
		var fwd, bwd int
		if boundary {
			fwd = (fieldPos - i + fs) % fs
			bwd = (fieldPos + i) % fs
		} else {
			fwd = (fieldPos + i) % fs
			bwd = (fieldPos - i + fs) % fs
		}
		v := n.Field[fwd] + lift*w
		if v > 1.0 {
			v = 1.0
		}
		n.Field[fwd] = v
		v2 := n.Field[bwd] + lift*w
		if v2 > 1.0 {
			v2 = 1.0
		}
		n.Field[bwd] = v2
	}
	n.Activations++
	n.Successes++
	c := spiralCoil(fieldPos)
	n.CoilHits[c]++
}

func (n *Neuron) dent(fieldPos int) {
	fs := fieldSize()
	r := coilRadius(fieldPos)
	boundary := isCoilBoundary(fieldPos)
	for i := 0; i < r; i++ {
		w := float32(1.0 - float64(i)/float64(r))
		var fwd, bwd int
		if boundary {
			fwd = (fieldPos - i + fs) % fs
			bwd = (fieldPos + i) % fs
		} else {
			fwd = (fieldPos + i) % fs
			bwd = (fieldPos - i + fs) % fs
		}
		v := n.Field[fwd] - 0.3*w
		if v < -1.0 {
			v = -1.0
		}
		n.Field[fwd] = v
		v2 := n.Field[bwd] - 0.3*w
		if v2 < -1.0 {
			v2 = -1.0
		}
		n.Field[bwd] = v2
	}
	n.Failures++
}

func (n *Neuron) dull(fieldPos int) {
	fs := fieldSize()
	r := int(float64(coilRadius(fieldPos)) * 0.6)
	for i := 0; i < r; i++ {
		w := float32(1.0 - float64(i)/float64(r))
		fwd := (fieldPos + i) % fs
		bwd := (fieldPos - i + fs) % fs
		n.Field[fwd] *= (1 - 0.08*w)
		n.Field[bwd] *= (1 - 0.08*w)
	}
}

func (n *Neuron) dominantCoil() int {
	best, bestK := 0, 0
	for k, h := range n.CoilHits {
		if h > best {
			best = h
			bestK = k
		}
	}
	return bestK
}

func (n *Neuron) storeCluster(chain []ChainLink) {
	if len(n.Clusters) >= ClusterMax {
		n.Clusters = n.Clusters[1:]
	}
	c := make([]ChainLink, len(chain))
	copy(c, chain)
	n.Clusters = append(n.Clusters, c)
}

// ============================================================================
// SACK ENGINE
// ============================================================================

const (
	ChainMinElastic = 8
	ChainMaxElastic = 32
)

func elasticChainMax(conf float64) int {
	t := 1.0 - math.Min(1.0, math.Max(0.0, conf))
	return ChainMinElastic + int(t*float64(ChainMaxElastic-ChainMinElastic))
}

type CheckHistoryEntry struct {
	NID      int
	FieldPos int
	Boundary bool
	Coil     int
}

type SACKEngine struct {
	Name          string
	Neurons       []*Neuron
	CellIndex     map[int][]int
	Chain         []ChainLink
	LastConf      float64
	TotalChains   int
	LastSpiralPos int
	CheckHistory  []CheckHistoryEntry
	// Rolling chain signature — updated every addLink call
	// This is what subsacks use to identify causal sequences
	ChainSig uint64

	// Resonance — composite subsack signal of the last fired neuron.
	// Mirrored from Neurons[nid].Resonance after each addLink so the
	// DelegatorSack can fold all piece children into ComboResonance.
	Resonance uint64

	// Tomb — wedge memory structure
	Tomb         *Tomb
	LastPulse    PulseResult
	LastPressure [CoarseCells]float32
}

func newSACK(name string) *SACKEngine {
	s := &SACKEngine{
		Name:      name,
		CellIndex: make(map[int][]int),
		Tomb:      newTomb(),
	}
	for i := 0; i < NeuronCount; i++ {
		s.Neurons = append(s.Neurons, newNeuron(i))
	}
	s.rebuildIndex()
	return s
}

func (s *SACKEngine) rebuildIndex() {
	s.CellIndex = make(map[int][]int)
	fs := fieldSize()
	for _, n := range s.Neurons {
		c := int(float64(n.PeakPos)/float64(fs)*CoarseCells) % CoarseCells
		s.CellIndex[c] = append(s.CellIndex[c], n.NID)
	}
}

func (s *SACKEngine) queryField(fieldPos int) (int, float64) {
	coilIdx := int(float64(fieldPos)/float64(fieldSize())*CoarseCells) % CoarseCells
	seen := make(map[int]bool)
	var candidates []int
	for c := coilIdx - 1; c <= coilIdx+1; c++ {
		cc := (c + CoarseCells) % CoarseCells
		for _, nid := range s.CellIndex[cc] {
			if !seen[nid] {
				seen[nid] = true
				candidates = append(candidates, nid)
			}
		}
	}
	if len(candidates) < 4 {
		for i := 0; i < NeuronCount; i++ {
			if !seen[i] {
				candidates = append(candidates, i)
			}
		}
	}
	best := math.Inf(-1)
	bestNid := 0
	for _, nid := range candidates {
		r := s.Neurons[nid].resonate(fieldPos)
		if r > best {
			best = r
			bestNid = nid
		}
	}
	return bestNid, best
}

// addLink advances the rolling chain signature, then fires subsacks
// with both the pressure state and the current chain sig.
// Subsacks now know exactly what sequence of play led to this moment.
func (s *SACKEngine) addLink(nid, fieldPos int, before, after [CoarseCells]float32) {
	var total float64
	for i := 0; i < CoarseCells; i++ {
		d := math.Abs(float64(after[i] - before[i]))
		total += d
	}
	delta := total / CoarseCells
	coil := spiralCoil(fieldPos)
	boundary := isCoilBoundary(fieldPos)
	s.LastSpiralPos = fieldPos

	// Advance rolling chain signature with this neuron's ID
	s.ChainSig = chainSigStep(s.ChainSig, nid)

	// Fire subsacks — pass current chain sig so they can track recurrence
	s.Neurons[nid].activateSubSacks(fieldPos, after, s.ChainSig)

	// Mirror the fired neuron's resonance to the engine level so the
	// DelegatorSack can aggregate across all piece children.
	s.Resonance = s.Neurons[nid].Resonance

	// Tomb pulse — new board state ripples through memory, harvesting resonances.
	// RecognitionConf feeds back into move scoring via LastPulse.
	s.LastPulse = s.Tomb.Pulse(s.Resonance, after)
	s.LastPressure = after

	link := ChainLink{
		NID: nid, FieldPos: fieldPos,
		Delta: delta, Coil: coil, Boundary: boundary,
	}
	s.Chain = append(s.Chain, link)

	s.CheckHistory = append(s.CheckHistory, CheckHistoryEntry{
		NID: nid, FieldPos: fieldPos, Boundary: boundary, Coil: coil,
	})
	if len(s.CheckHistory) > 8 {
		s.CheckHistory = s.CheckHistory[len(s.CheckHistory)-8:]
	}

	// v17 COMPLIANT: Push clusters to the neurons to track internal frequency,
	// but do NOT truncate s.Chain. Let it accumulate for the entire game duration.
	chainMax := elasticChainMax(s.LastConf)
	if len(s.Chain) % chainMax == 0 {
		s.Neurons[nid].storeCluster(s.Chain)
		s.TotalChains++
	}
}

func (s *SACKEngine) reinforceOutcome(outcome float64) {
	chain := s.Chain
	l := len(chain)
	if l == 0 {
		return
	}
	for i, link := range chain {
		n := s.Neurons[link.NID]
		recency := float64(i+1) / float64(l)
		boundaryAmp := 1.0
		if link.Boundary {
			boundaryAmp = 1.5
		}
		coilWeight := 1.0 + float64(link.Coil)/SpiralCoils*0.5
		if outcome > 0 {
			n.bounce(link.FieldPos, recency*(1+link.Delta*2)*boundaryAmp*coilWeight)
		} else if outcome < 0 {
			// v17: No punishment cascade on loss.
			// Loss learning is handled by the adversarial tomb — opponent's
			// winning board states are stored there and penalise similar future
			// move candidates during scoring. The field is left undamaged:
			// a losing game visited valid positions; those positions are still
			// worth reaching in future games that go differently.
		} else {
			n.dull(link.FieldPos) // draw: mild decay, no strong signal
		}
	}
	if outcome > 0 {
		s.Neurons[chain[l-1].NID].storeCluster(chain)
		s.TotalChains++
		// Deposit winning chain into the tomb for future pulse matching.
		// Composite resonance across all neurons in the chain.
		var compositeRes uint64
		for _, link := range chain {
			compositeRes ^= s.Neurons[link.NID].Resonance
		}
		s.Tomb.Store(chain, s.ChainSig, compositeRes, s.LastPressure)
	}
	s.Chain = nil
	s.ChainSig = 0 // reset chain sig on game end
	s.rebuildIndex()
}

// reinforceCapture fires a flat-weight bounce on the last chain link when a
// piece is captured mid-game. Weight is uniform across all piece types — the
// tomb earns its own opinion about capture value by observing which captures
// appear in winning chains. No injected piece-value ordering.
func (s *SACKEngine) reinforceCapture(weight float64) {
	l := len(s.Chain)
	if l == 0 {
		return
	}
	last := s.Chain[l-1]
	s.Neurons[last.NID].bounce(last.FieldPos, weight)
}

func (s *SACKEngine) resetChain() {
	s.Chain = nil
	s.ChainSig = 0
}

// subsackReport returns network-wide growth profile
func (s *SACKEngine) subsackReport() map[int]int {
	totals := make(map[int]int)
	for _, n := range s.Neurons {
		for bits, count := range n.growthProfile() {
			totals[bits] += count
		}
	}
	return totals
}

// ============================================================================
// STATS & SERIALISATION
// ============================================================================
type SubSackData struct {
	GrowthBits int    `json:"growthBits"`
	TopHits    int    `json:"topHits"`
	Signal     uint64 `json:"signal"`
}

type NeuronData struct {
	Activations int            `json:"activations"`
	Successes   float64        `json:"successes"`
	Failures    float64        `json:"failures"`
	PeakPos     int            `json:"peakPos"`
	Field       []float32      `json:"field"`
	CoilHits    map[string]int `json:"coilHits"`
	IsBoundary  bool           `json:"isBoundary"`
	SubSacks    []SubSackData  `json:"subSacks"`
	Resonance   uint64         `json:"resonance"`
}

type SACKStats struct {
	Active        int          `json:"active"`
	Dead          int          `json:"dead"`
	Conf          float64      `json:"conf"`
	ChainLen      int          `json:"chainLen"`
	TotalChains   int          `json:"totalChains"`
	SpiralPos     int          `json:"spiralPos"`
	Neurons       []NeuronData `json:"neurons"`
	SubSackGrowth map[int]int  `json:"subSackGrowth"`
	RouteCount    int          `json:"routeCount"` // how many moves routed to this child
}

func (s *SACKEngine) stats() SACKStats {
	var active, dead int
	nData := s.serialise()
	for _, n := range s.Neurons {
		if n.Activations == 0 {
			dead++
		} else {
			active++
		}
	}
	return SACKStats{
		Active:        active,
		Dead:          dead,
		Conf:          s.LastConf,
		ChainLen:      len(s.Chain),
		TotalChains:   s.TotalChains,
		SpiralPos:     s.LastSpiralPos,
		Neurons:       nData,
		SubSackGrowth: s.subsackReport(),
	}
}

func (s *SACKEngine) serialise() []NeuronData {
	data := make([]NeuronData, len(s.Neurons))
	for i, n := range s.Neurons {
		ch := make(map[string]int)
		for k, v := range n.CoilHits {
			ch[itoa(k)] = v
		}
		ssd := make([]SubSackData, len(n.SubSacks))
		for j, ss := range n.SubSacks {
			ssd[j] = SubSackData{
				GrowthBits: ss.GrowthBits,
				TopHits:    ss.TopHits,
				Signal:     ss.Signal,
			}
		}
		data[i] = NeuronData{
			Activations: n.Activations,
			Successes:   n.Successes,
			Failures:    n.Failures,
			PeakPos:     n.PeakPos,
			Field:       append([]float32{}, n.Field...),
			CoilHits:    ch,
			IsBoundary:  n.IsBoundary,
			SubSacks:    ssd,
			Resonance:   n.Resonance,
		}
	}
	return data
}

func (s *SACKEngine) deserialise(data []NeuronData) {
	lim := len(data)
	if lim > len(s.Neurons) {
		lim = len(s.Neurons)
	}
	for i := 0; i < lim; i++ {
		d := data[i]
		n := s.Neurons[i]
		n.Activations = d.Activations
		n.Successes = d.Successes
		n.Failures = d.Failures
		n.PeakPos = d.PeakPos
		n.Field = make([]float32, len(d.Field))
		copy(n.Field, d.Field)
		n.CoilHits = make(map[int]int)
		for k, v := range d.CoilHits {
			n.CoilHits[atoi(k)] = v
		}
		n.IsBoundary = d.IsBoundary
		n.Resonance = d.Resonance
		if len(d.SubSacks) > 0 {
			n.SubSacks = make([]SubSack, len(d.SubSacks))
			for j, ssd := range d.SubSacks {
				n.SubSacks[j] = SubSack{
					GrowthBits: ssd.GrowthBits,
					TopHits:    ssd.TopHits,
					Signal:     ssd.Signal,
					ChainHits:  make(map[uint64]int),
				}
			}
		}
	}
	s.rebuildIndex()
}

func itoa(i int) string {
	return fmt.Sprintf("%d", i)
}

func atoi(s string) int {
	var i int
	fmt.Sscanf(s, "%d", &i)
	return i
}

// ============================================================================
// PIECE PRESSURE SNAPSHOT
// Like pressureSnapshot but centred on any piece's square, not the king.
// Each piece-type child sack sees the board from its own piece's vantage —
// giving it genuinely specialised positional knowledge.
// ============================================================================

func piecePressureSnapshot(b Board, color string, pr, pf int) [CoarseCells]float32 {
	var bins [CoarseCells]float32
	for r := 0; r < 8; r++ {
		for f := 0; f < 8; f++ {
			p := b[r][f]
			if p == nil || (r == pr && f == pf) {
				continue
			}
			angle := math.Atan2(float64(r-pr), float64(f-pf))
			angleDeg := math.Mod(angle*180/math.Pi+180, 180)
			cell := int(angleDeg/180.0*CoarseCells) % CoarseCells
			dist := math.Hypot(float64(f-pf), float64(r-pr))
			weight := float32(pieceValue[p.Type] / (dist + 1))
			if p.Color == color {
				bins[cell] += weight
			} else {
				bins[cell] -= weight
			}
		}
	}
	var maxV float32
	for i := 0; i < CoarseCells; i++ {
		v := bins[i]
		if v < 0 {
			v = -v
		}
		if v > maxV {
			maxV = v
		}
	}
	if maxV > 0 {
		for i := 0; i < CoarseCells; i++ {
			bins[i] /= maxV
		}
	}
	return bins
}

// ============================================================================
// DELEGATOR SACK — piece-level controller
// ============================================================================
//
// The DelegatorSack manages one child SACKEngine per chess piece type.
// Children are spawned lazily — the first time a piece type makes a move.
//
// Piece-type order (fixed, 6 slots):
//   0=K  1=Q  2=R  3=B  4=N  5=P
//
// Each child learns the movement patterns and positional pressures specific
// to its piece type. The delegator aggregates all active children's resonance
// signals into a ComboResonance — the combined position fingerprint the
// delegator "learns" to associate with good or bad board states.
// ============================================================================

var pieceChildOrder = []string{"K", "Q", "R", "B", "N", "P"}

func pieceChildIndex(pType string) int {
	for i, t := range pieceChildOrder {
		if t == pType {
			return i
		}
	}
	return -1 // unknown type
}

// pieceAngle generates a stable, well-distributed rotation constant for each
// piece-type slot. Applied as XOR to ComboResonance before tomb storage/query,
// this lets the shared tomb distinguish piece-type contexts while still sharing
// board-state knowledge through the pressure channel.
//
// Uses successive applications of the Fibonacci/golden-ratio hash step so
// that angles for adjacent piece types differ maximally in bit distribution.
func pieceAngle(idx int) uint64 {
	v := uint64(idx+1) * 0x9e3779b97f4a7c15
	v ^= v >> 33
	v *= 0xff51afd7ed558ccd
	v ^= v >> 33
	v *= 0xc4ceb9fe1a85ec53
	v ^= v >> 33
	return v
}

type DelegatorSack struct {
	Name      string
	// One child per piece type; pre-spawned at creation, never nil.
	Children  [6]*SACKEngine
	// Index of the child that handled the most recent actual move (-1 = none yet).
	ActiveIdx int
	SpawnCount  int
	TotalRoutes int
	// Per-child route counter — how many actual moves each piece type has made.
	// Used for exploration bonus in scoring (unexplored piece types get a bump).
	ChildRouteCounts [6]int
	// Aggregate resonance across all active children.
	// Updated after every move — this is the combined position fingerprint.
	ComboResonance uint64

	// SharedTomb — a single tomb shared across all piece-type children.
	// Chains are stored with ComboResonance ^ pieceAngle(pieceIdx) so each piece
	// type occupies a distinct but overlapping region of the resonance space.
	// This lets patterns bleed between piece types through the pressure channel
	// while the angle rotation keeps direct-query results piece-type-specific.
	SharedTomb *Tomb

	// AdversarialTomb — stores the opponent's winning board-state signatures.
	// Fed from the opponent's pressure history at game end (on loss only).
	// When scoring candidate moves, high recognition confidence means the move
	// leads toward a board configuration the opponent dominated before →
	// apply a scoring penalty to steer away without explicit rules.
	// The system discovers what to avoid through felt experience, not programming.
	AdversarialTomb *Tomb

	// Mirrors from the active child — kept in sync after each route.
	LastConf  float64
	LastPulse PulseResult
	// Last result from the shared tomb — updated on every actual move pulse.
	SharedPulse PulseResult
	// Last result from the adversarial tomb — updated when a loss is stored.
	AdversarialPulse PulseResult
}

func newDelegatorSack(name string) *DelegatorSack {
	d := &DelegatorSack{
		Name:            name,
		ActiveIdx:       -1,
		SharedTomb:      newTomb(),
		AdversarialTomb: newTomb(),
	}
	// Pre-spawn all 6 piece-type children so they are always present.
	// Without eager spawn the King monopoly prevents other children from ever
	// being routed to — the children show as dead in the UI and never learn.
	for i, pType := range pieceChildOrder {
		d.Children[i] = newSACK(fmt.Sprintf("%s-%s", d.Name, pType))
	}
	d.SpawnCount = 6
	return d
}

// childFor returns the child for the given piece type, spawning it on first use.
func (d *DelegatorSack) childFor(pType string) (*SACKEngine, int) {
	idx := pieceChildIndex(pType)
	if idx < 0 {
		idx = 5 // fallback to pawn slot for unknown types
	}
	if d.Children[idx] == nil {
		d.Children[idx] = newSACK(fmt.Sprintf("%s-%s", d.Name, pType))
		d.SpawnCount++
	}
	return d.Children[idx], idx
}

// routeMove routes the given move to the correct piece-type child (hard route).
// Sets ActiveIdx and syncs LastConf/LastPulse from the chosen child.
// Called for actual moves; updates delegator state.
func (d *DelegatorSack) routeMove(board Board, move Move) (*SACKEngine, int) {
	pType := "P"
	if p := board[move.From[0]][move.From[1]]; p != nil {
		pType = p.Type
	}
	child, idx := d.childFor(pType)
	d.ActiveIdx = idx
	d.TotalRoutes++
	d.ChildRouteCounts[idx]++
	d.syncFromChild(child)
	return child, idx
}

// peekMove — read-only route for move-scoring hypotheticals.
// Does NOT update ActiveIdx or TotalRoutes.
// If the relevant child hasn't been spawned yet, returns any existing child.
func (d *DelegatorSack) peekMove(board Board, move Move) *SACKEngine {
	pType := "P"
	if p := board[move.From[0]][move.From[1]]; p != nil {
		pType = p.Type
	}
	idx := pieceChildIndex(pType)
	if idx < 0 {
		idx = 5
	}
	if d.Children[idx] != nil {
		return d.Children[idx]
	}
	// Child not yet spawned — fall back to any active child
	for _, c := range d.Children {
		if c != nil {
			return c
		}
	}
	return nil
}

// activeChild returns the child set by the last routeMove call.
func (d *DelegatorSack) activeChild() *SACKEngine {
	if d.ActiveIdx >= 0 && d.ActiveIdx < 6 {
		if c := d.Children[d.ActiveIdx]; c != nil {
			return c
		}
	}
	for _, c := range d.Children {
		if c != nil {
			return c
		}
	}
	return nil
}

// syncFromChild mirrors LastConf and LastPulse from the given child.
func (d *DelegatorSack) syncFromChild(child *SACKEngine) {
	if child == nil {
		return
	}
	d.LastConf = child.LastConf
	d.LastPulse = child.LastPulse
}

// updateCombo recomputes ComboResonance as a XOR fold of all active children's
// resonance signals. Called after every move so the delegator always holds
// the current combined position fingerprint.
func (d *DelegatorSack) updateCombo() {
	var combo uint64
	for i, c := range d.Children {
		if c != nil {
			// Rotate each child's contribution by its slot index to avoid collisions
			r := c.Resonance
			shift := uint(i * 10 % 64)
			combo ^= (r << shift) | (r >> (64 - shift))
		}
	}
	d.ComboResonance = combo
}

// ============================================================================
// SHARED TOMB — angle-differentiated cross-piece memory
// ============================================================================

// sharedQuery returns the rotated resonance key for querying/storing piece idx.
// Same board state + same piece type  → identical key  (strong match).
// Same board state + different piece  → different key  (partial match via pressure).
// Different board state               → low match regardless of piece.
func (d *DelegatorSack) sharedQuery(pieceIdx int) uint64 {
	return d.ComboResonance ^ pieceAngle(pieceIdx)
}

// sharedTombConf performs a read-only peek at the shared tomb for the given
// piece type. Returns RecognitionConf 0..1. No side effects — safe to call
// for every candidate move during scoring.
func (d *DelegatorSack) sharedTombConf(pieceIdx int, pressure [CoarseCells]float32) float64 {
	if d.SharedTomb == nil || d.ComboResonance == 0 {
		return 0
	}
	return d.SharedTomb.PeekMatch(d.sharedQuery(pieceIdx), pressure)
}

// pulseSharedTomb does a full Pulse on the shared tomb — called once per actual
// move (not during hypothetical scoring). Updates SharedPulse.
func (d *DelegatorSack) pulseSharedTomb(pieceIdx int, pressure [CoarseCells]float32) {
	if d.SharedTomb == nil {
		return
	}
	d.SharedPulse = d.SharedTomb.Pulse(d.sharedQuery(pieceIdx), pressure)
}

// storeToSharedTomb deposits a winning chain into the shared tomb.
// Called from reinforceOutcome BEFORE the child clears its chain.
func (d *DelegatorSack) storeToSharedTomb(pieceIdx int, chain []ChainLink, chainSig uint64, pressure [CoarseCells]float32) {
	if d.SharedTomb == nil || len(chain) == 0 {
		return
	}
	rotatedRes := d.sharedQuery(pieceIdx)
	d.SharedTomb.Store(chain, chainSig, rotatedRes, pressure)
}

// ============================================================================
// DELEGATION METHODS — forward to the active child
// ============================================================================

func (d *DelegatorSack) addLink(nid, fieldPos int, before, after [CoarseCells]float32) {
	child := d.activeChild()
	if child == nil {
		return
	}
	child.addLink(nid, fieldPos, before, after)
	d.syncFromChild(child)
	d.updateCombo()
}

func (d *DelegatorSack) reinforceOutcome(outcome float64) {
	child := d.activeChild()
	if child == nil {
		return
	}
	if outcome > 0 && len(child.Chain) > 0 {
		// Capture the winning chain BEFORE reinforceOutcome clears it,
		// so we can also deposit it into the shared tomb.
		chain := make([]ChainLink, len(child.Chain))
		copy(chain, child.Chain)
		chainSig := child.ChainSig
		pressure := child.LastPressure

		child.reinforceOutcome(outcome)

		// Store into shared tomb with piece-angle-rotated resonance.
		// The Knight's win on this board state leaves a faint signal
		// that the Rook will partially detect at similar board states.
		d.storeToSharedTomb(d.ActiveIdx, chain, chainSig, pressure)
	} else {
		child.reinforceOutcome(outcome)
	}
}

// reinforceCapture routes a flat-weight mid-game capture bounce to the active
// piece child. Flat weight only — no pieceValue scaling. The tomb discovers
// which captures matter most by observing their frequency in winning chains.
func (d *DelegatorSack) reinforceCapture(weight float64) {
	if child := d.activeChild(); child != nil {
		child.reinforceCapture(weight)
	}
}

// storeAdversarial encodes the opponent's recent board states as an adversarial
// pattern and deposits it into the adversarial tomb. Called once per loss.
// pressures is a rolling window of king-centric pressure snapshots taken after
// each of the opponent's moves during the losing game.
func (d *DelegatorSack) storeAdversarial(pressures [][CoarseCells]float32) {
	if d.AdversarialTomb == nil || len(pressures) == 0 {
		return
	}
	resonance := pressuresToResonance(pressures)
	lastPressure := pressures[len(pressures)-1]
	// Store as a patternless chain (no links) — just the resonance fingerprint
	// and the terminal board pressure. The tomb's matchScore uses both the
	// resonance (primary) and pressure (secondary) when queried, so future
	// board states that feel like this losing pattern will be caught.
	d.AdversarialTomb.Store(nil, resonance, resonance, lastPressure)
}

// peekAdversarial returns a recognition confidence 0..1 from the adversarial
// tomb. High confidence means this board state resembles a previously lost
// game pattern. Used in scoreMove to penalise moves into known bad territory.
func (d *DelegatorSack) peekAdversarial(resonance uint64, pressure [CoarseCells]float32) float64 {
	if d.AdversarialTomb == nil {
		return 0
	}
	return d.AdversarialTomb.PeekMatch(resonance, pressure)
}

func (d *DelegatorSack) resetChain() {
	for _, c := range d.Children {
		if c != nil {
			c.resetChain()
		}
	}
}

func (d *DelegatorSack) totalChains() int {
	total := 0
	for _, c := range d.Children {
		if c != nil {
			total += c.TotalChains
		}
	}
	return total
}

// tombStats returns the active child's tomb stats for display.
func (d *DelegatorSack) tombStats() TombStats {
	if child := d.activeChild(); child != nil {
		return child.Tomb.Stats()
	}
	return TombStats{}
}

// ============================================================================
// DELEGATOR STATS
// ============================================================================

type DelegatorStats struct {
	Name             string     `json:"name"`
	SpawnCount       int        `json:"spawnCount"`
	TotalRoutes      int        `json:"totalRoutes"`
	ActiveChildIdx   int        `json:"activeChildIdx"`
	ActivePiece      string     `json:"activePiece"`
	TotalChains      int        `json:"totalChains"`
	ComboResonance   uint64     `json:"comboResonance"`
	SharedTomb       TombStats  `json:"sharedTomb"`
	AdversarialTomb  TombStats  `json:"adversarialTomb"`
	Children         []SACKStats `json:"children,omitempty"`
}

func (d *DelegatorSack) stats() DelegatorStats {
	activePiece := ""
	if d.ActiveIdx >= 0 && d.ActiveIdx < len(pieceChildOrder) {
		activePiece = pieceChildOrder[d.ActiveIdx]
	}
	children := make([]SACKStats, 6)
	for i, c := range d.Children {
		if c != nil {
			s := c.stats()
			s.RouteCount = d.ChildRouteCounts[i]
			children[i] = s
		}
	}
	var sharedTombStats TombStats
	if d.SharedTomb != nil {
		sharedTombStats = d.SharedTomb.Stats()
	}
	var adversarialTombStats TombStats
	if d.AdversarialTomb != nil {
		adversarialTombStats = d.AdversarialTomb.Stats()
	}
	return DelegatorStats{
		Name:            d.Name,
		SpawnCount:      d.SpawnCount,
		TotalRoutes:     d.TotalRoutes,
		ActiveChildIdx:  d.ActiveIdx,
		ActivePiece:     activePiece,
		TotalChains:     d.totalChains(),
		ComboResonance:  d.ComboResonance,
		SharedTomb:      sharedTombStats,
		AdversarialTomb: adversarialTombStats,
		Children:        children,
	}
}

// activeChildStats returns the active child's SACKStats (for backward-compat fields).
func (d *DelegatorSack) activeChildStats() SACKStats {
	if child := d.activeChild(); child != nil {
		return child.stats()
	}
	return SACKStats{}
}

// ============================================================================
// DELEGATOR SERIALISATION
// ============================================================================

type DelegatorChildSave struct {
	PieceType   string       `json:"pieceType"`
	Neurons     []NeuronData `json:"neurons"`
	TotalChains int          `json:"totalChains"`
}

type DelegatorSave struct {
	Name        string               `json:"name"`
	Children    []DelegatorChildSave `json:"children"`
	SpawnCount  int                  `json:"spawnCount"`
	TotalRoutes int                  `json:"totalRoutes"`
	ActiveIdx   int                  `json:"activeIdx"`
}

func (d *DelegatorSack) serialiseDelegate() DelegatorSave {
	var children []DelegatorChildSave
	for i, pType := range pieceChildOrder {
		c := d.Children[i]
		if c == nil {
			continue
		}
		children = append(children, DelegatorChildSave{
			PieceType:   pType,
			Neurons:     c.serialise(),
			TotalChains: c.TotalChains,
		})
	}
	return DelegatorSave{
		Name:        d.Name,
		Children:    children,
		SpawnCount:  d.SpawnCount,
		TotalRoutes: d.TotalRoutes,
		ActiveIdx:   d.ActiveIdx,
	}
}

func (d *DelegatorSack) deserialiseDelegate(save DelegatorSave) {
	d.SpawnCount = save.SpawnCount
	d.TotalRoutes = save.TotalRoutes
	d.ActiveIdx = save.ActiveIdx
	for _, cs := range save.Children {
		idx := pieceChildIndex(cs.PieceType)
		if idx < 0 {
			continue
		}
		child := newSACK(fmt.Sprintf("%s-%s", d.Name, cs.PieceType))
		child.deserialise(cs.Neurons)
		child.TotalChains = cs.TotalChains
		d.Children[idx] = child
	}
	d.updateCombo()
}