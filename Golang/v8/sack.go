package main

import (
	"fmt"
	"math"
	"math/rand"
)

// ============================================================================
// SACK — v6
// Spiral Associative Cortex Kernel
// Ported directly from v4 JS. Same field geometry, same learning rules.
// ============================================================================

const (
	NeuronCount = 
	ChainMax    = 32
	ClusterMax  = 32
	CoarseCells = 16
	SpiralCoils = 8
	SpiralBase  = 8
)

// Spiral map — precomputed at init
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
// PRESSURE SNAPSHOT — 16 angular bins from king perspective
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

func deltaToSpiralPos(before, after [CoarseCells]float32) int {
	var total, dirSum float64
	var maxD float64
	maxCell := 0
	for i := 0; i < CoarseCells; i++ {
		d := math.Abs(float64(after[i] - before[i]))
		total += d
		dirSum += float64(after[i] - before[i])
		if d > maxD {
			maxD = d
			maxCell = i
		}
	}
	avg := total / CoarseCells
	direction := 0
	if dirSum > 0 {
		direction = 1
	}
	normVal := 1.0 - math.Min(1.0, avg*3)
	targetCoil := int(normVal * SpiralCoils)
	if targetCoil >= SpiralCoils {
		targetCoil = SpiralCoils - 1
	}
	start := spiralMap.Starts[targetCoil]
	size := spiralMap.Sizes[targetCoil]
	fraction := normVal*SpiralCoils - float64(targetCoil)
	angOffset := int(float64(maxCell) / CoarseCells * float64(size))
	pos := (start + int(fraction*float64(size)) + angOffset + direction) % fieldSize()
	if isCoilBoundary(pos) {
		c := spiralCoil(pos)
		cs := spiralMap.Starts[c]
		cz := spiralMap.Sizes[c]
		pos = cs + (cz - 1 - (pos - cs))
	}
	fs := fieldSize()
	return ((pos % fs) + fs) % fs
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
}

func newNeuron(nid int) *Neuron {
	fs := fieldSize()
	n := &Neuron{
		NID:       nid,
		Field:     make([]float32, fs),
		Successes: 1,
		CoilHits:  make(map[int]int),
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
	ChainMinElastic = 8  // floor when confidence is high
	ChainMaxElastic = 32 // ceiling when confidence is low
)

// elasticChainMax returns a chain flush length based on recent confidence.
// High confidence → shorter chains (pattern known), low → longer (exploring).
func elasticChainMax(conf float64) int {
	// conf is 0..1; invert so low conf = long chain
	t := 1.0 - math.Min(1.0, math.Max(0.0, conf))
	return ChainMinElastic + int(t*float64(ChainMaxElastic-ChainMinElastic))
}

// CheckHistoryEntry records a (neuron, fieldPos) pair for gradient replay.
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
	// Rolling history for gradient-backward check reward (last 8 links)
	CheckHistory  []CheckHistoryEntry
}

func newSACK(name string) *SACKEngine {
	s := &SACKEngine{
		Name:      name,
		CellIndex: make(map[int][]int),
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

	link := ChainLink{
		NID: nid, FieldPos: fieldPos,
		Delta: delta, Coil: coil, Boundary: boundary,
	}
	s.Chain = append(s.Chain, link)

	// Rolling check history — keep last 8 entries
	s.CheckHistory = append(s.CheckHistory, CheckHistoryEntry{
		NID: nid, FieldPos: fieldPos, Boundary: boundary, Coil: coil,
	})
	if len(s.CheckHistory) > 8 {
		s.CheckHistory = s.CheckHistory[len(s.CheckHistory)-8:]
	}

	// Elastic flush threshold based on current confidence
	chainMax := elasticChainMax(s.LastConf)
	if len(s.Chain) >= chainMax {
		s.Neurons[nid].storeCluster(s.Chain)
		s.TotalChains++
		s.Chain = nil
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
			if i == l-1 {
				n.dent(link.FieldPos)
			} else if i >= l-3 {
				n.dull(link.FieldPos)
			}
			n.Failures++
		} else {
			n.dull(link.FieldPos)
		}
	}
	if outcome > 0 {
		s.Neurons[chain[l-1].NID].storeCluster(chain)
		s.TotalChains++
	}
	s.Chain = nil
	s.rebuildIndex()
}

// reinforceCheckGradient fires a linear-decay reward backward through the
// last 8 moves leading to the check. Check move = 1.0, 7 moves back = 0.125.
func (s *SACKEngine) reinforceCheckGradient() {
	l := len(s.CheckHistory)
	if l == 0 {
		return
	}
	for i, entry := range s.CheckHistory {
		// i=0 is oldest, i=l-1 is the check move itself
		reward := float64(i+1) / float64(l) // linear 1/l … l/l
		boundaryAmp := 1.0
		if entry.Boundary {
			boundaryAmp = 1.4
		}
		coilWeight := 1.0 + float64(entry.Coil)/SpiralCoils*0.4
		strength := reward * boundaryAmp * coilWeight * 0.5 // dampened vs win reinforce
		s.Neurons[entry.NID].bounce(entry.FieldPos, strength)
	}
}

func (s *SACKEngine) reinforceCheck() {
	l := len(s.Chain)
	if l == 0 {
		return
	}
	last := s.Chain[l-1]
	s.Neurons[last.NID].bounce(last.FieldPos, 0.4)
	// Also apply gradient through recent history
	s.reinforceCheckGradient()
}

func (s *SACKEngine) resetChain() { s.Chain = nil }

type SACKStats struct {
	Active      int          `json:"active"`
	Dead        int          `json:"dead"`
	Conf        float64      `json:"conf"`
	ChainLen    int          `json:"chainLen"`
	TotalChains int          `json:"totalChains"`
	SpiralPos   int          `json:"spiralPos"`
	Neurons     []NeuronData `json:"neurons"`
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
		Active:      active,
		Dead:        dead,
		Conf:        s.LastConf,
		ChainLen:    len(s.Chain),
		TotalChains: s.TotalChains,
		SpiralPos:   s.LastSpiralPos,
		Neurons:     nData,
	}
}

// ============================================================================
// SERIALISATION — JSON compatible with v4/v5 store format
// ============================================================================
type NeuronData struct {
	Activations int            `json:"activations"`
	Successes   float64        `json:"successes"`
	Failures    float64        `json:"failures"`
	PeakPos     int            `json:"peakPos"`
	Field       []float32      `json:"field"`
	CoilHits    map[string]int `json:"coilHits"`
	IsBoundary  bool           `json:"isBoundary"`
}

func (s *SACKEngine) serialise() []NeuronData {
	data := make([]NeuronData, len(s.Neurons))
	for i, n := range s.Neurons {
		ch := make(map[string]int)
		for k, v := range n.CoilHits {
			ch[itoa(k)] = v
		}
		data[i] = NeuronData{
			Activations: n.Activations,
			Successes:   n.Successes,
			Failures:    n.Failures,
			PeakPos:     n.PeakPos,
			Field:       append([]float32{}, n.Field...),
			CoilHits:    ch,
			IsBoundary:  n.IsBoundary,
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