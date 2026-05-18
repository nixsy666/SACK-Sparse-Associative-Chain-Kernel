package main

// ============================================================================
// TOMB PERSISTENCE — v1
// Binary blob serialisation for the Tomb wedge memory structure.
//
// Blob layout:
//   [8]  header  — last CompositeResonance (self-validating fingerprint)
//   [4]  version — format version number
//   [4]  totalStored
//   [4]  totalPulses
//   [4]  totalPromoted
//   per layer (TombDepths = 12):
//     [4] chainCount
//     per chain:
//       [8]  ChainSig
//       [8]  Resonance
//       [16*4] Pressure (CoarseCells float32s)
//       [4]  TombDepth
//       [4]  ReuseCount
//       [4]  Age
//       [4]  FragmentHits
//       [4]  linkCount
//       per link:
//         [4]  NID
//         [4]  FieldPos
//         [8]  Delta (float64)
//         [4]  Coil
//         [1]  Boundary (bool as byte)
//         [3]  padding (alignment)
//
// Fixed chain overhead: 8+8+64+4+4+4+4+4 = 100 bytes
// Per link: 4+4+8+4+1+3 = 24 bytes
// Typical chain with 16 links: 100 + 16*24 = 484 bytes
//
// Estimated blob size after 70 games: ~120-150kb vs ~750kb JSON
// ============================================================================

import (
	"encoding/binary"
	"math"
	"os"
	"time"
)

const (
	TombBlobVersion  = 1
	TombBlobMagic    = uint64(0xDEAD5ACB00000001) // sentinel if resonance is ever zero
)

// tombBlobPath returns the .tomb sibling path for a given cortex save file.
// e.g. "sack_cortex.json" → "sack_cortex_w.tomb" / "sack_cortex_b.tomb"
func tombBlobPath(saveFile, side string) string {
	// Strip extension if present, append side tag
	base := saveFile
	for i := len(saveFile) - 1; i >= 0; i-- {
		if saveFile[i] == '.' {
			base = saveFile[:i]
			break
		}
	}
	return base + "_" + side + ".tomb"
}

// ============================================================================
// WRITE
// ============================================================================

func writeTomb(t *Tomb, path string, headerResonance uint64) error {
	// Build in memory first — avoids partial writes to disk
	buf := make([]byte, 0, 256*1024)
	w := &byteWriter{buf: &buf}

	// Header — self-validating fingerprint
	hdr := headerResonance
	if hdr == 0 {
		hdr = TombBlobMagic
	}
	w.u64(hdr)
	w.u32(TombBlobVersion)
	w.u32(uint32(t.TotalStored))
	w.u32(uint32(t.TotalPulses))
	w.u32(uint32(t.TotalPromoted))

	for d := 0; d < TombDepths; d++ {
		layer := t.Layers[d]
		w.u32(uint32(len(layer.Chains)))
		for _, tc := range layer.Chains {
			w.u64(tc.ChainSig)
			w.u64(tc.Resonance)
			for i := 0; i < CoarseCells; i++ {
				w.u32(math.Float32bits(tc.Pressure[i]))
			}
			w.u32(uint32(tc.TombDepth))
			w.u32(uint32(tc.ReuseCount))
			w.u32(uint32(tc.Age))
			w.u32(uint32(tc.FragmentHits))
			w.u32(uint32(len(tc.Links)))
			for _, lk := range tc.Links {
				w.u32(uint32(lk.NID))
				w.u32(uint32(lk.FieldPos))
				w.u64(math.Float64bits(lk.Delta))
				w.u32(uint32(lk.Coil))
				if lk.Boundary {
					w.u8(1)
				} else {
					w.u8(0)
				}
				w.u8(0); w.u8(0); w.u8(0) // 3-byte alignment pad
			}
		}
	}

	// Atomic write: write to .tmp, verify size, rename over old
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, buf, 0644); err != nil {
		return err
	}
	// Verify tmp is readable and same size
	info, err := os.Stat(tmp)
	if err != nil || info.Size() != int64(len(buf)) {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

// ============================================================================
// READ
// ============================================================================

func readTomb(path string) (*Tomb, uint64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, 0, err
	}

	r := &byteReader{data: data}

	// Read and validate header
	hdr := r.u64()
	version := r.u32()
	if version != TombBlobVersion {
		// Unknown version — don't corrupt memory, just start fresh
		return nil, 0, nil
	}

	t := newTomb()
	t.TotalStored   = int(r.u32())
	t.TotalPulses   = int(r.u32())
	t.TotalPromoted = int(r.u32())

	for d := 0; d < TombDepths; d++ {
		chainCount := int(r.u32())
		for c := 0; c < chainCount; c++ {
			if r.err {
				break
			}
			tc := &TombChain{}
			tc.ChainSig   = r.u64()
			tc.Resonance  = r.u64()
			for i := 0; i < CoarseCells; i++ {
				tc.Pressure[i] = math.Float32frombits(r.u32())
			}
			tc.TombDepth   = int(r.u32())
			tc.ReuseCount  = int(r.u32())
			tc.Age         = int(r.u32())
			tc.FragmentHits = int(r.u32())
			linkCount := int(r.u32())
			tc.Links = make([]ChainLink, linkCount)
			for i := 0; i < linkCount; i++ {
				tc.Links[i].NID      = int(r.u32())
				tc.Links[i].FieldPos = int(r.u32())
				tc.Links[i].Delta    = math.Float64frombits(r.u64())
				tc.Links[i].Coil     = int(r.u32())
				b := r.u8()
				tc.Links[i].Boundary = b != 0
				r.u8(); r.u8(); r.u8() // consume padding
			}
			// Place directly at correct depth — bypass store() eviction
			// since we're restoring known-good state
			layer := t.Layers[d]
			layer.Chains = append(layer.Chains, tc)
		}
	}

	if r.err {
		// Partial read — blob was corrupt or truncated
		return nil, hdr, nil
	}

	return t, hdr, nil
}

// ============================================================================
// VALIDATE
// Pulse the header resonance back through the restored tomb.
// If the tomb recognises its own last fingerprint, the blob is coherent.
// ============================================================================

func validateTomb(t *Tomb, headerResonance uint64) bool {
	if t == nil {
		return false
	}
	if headerResonance == TombBlobMagic {
		// Empty tomb blob — valid but no chains to validate against
		return true
	}
	// Pulse the header sig through — use zero pressure (neutral)
	var zeroPressure [CoarseCells]float32
	result := t.Pulse(headerResonance, zeroPressure)
	// A valid tomb should recognise its own last composite at some confidence
	return result.RecognitionConf > 0.0 || result.TopScore > 0.0
}

// ============================================================================
// SERVER INTEGRATION
// Call saveTombs() after maybeSave() on a win.
// Call loadTombs() inside tryLoad() after cortex is loaded.
// ============================================================================

// tombChildPath returns the blob path for a specific piece child's tomb.
// e.g. "sack_cortex.json", "w", "K" → "sack_cortex_w_K.tomb"
func tombChildPath(saveFile, side, pieceType string) string {
	base := saveFile
	for i := len(saveFile) - 1; i >= 0; i-- {
		if saveFile[i] == '.' {
			base = saveFile[:i]
			break
		}
	}
	return base + "_" + side + "_" + pieceType + ".tomb"
}

func (srv *Server) saveTombs() {
	if srv.saveFile == "" {
		return
	}
	// Brief pause so cortex JSON flushes first
	time.Sleep(150 * time.Millisecond)

	type colorEntry struct {
		side  string
		deleg *DelegatorSack
	}
	for _, ce := range []colorEntry{{"w", srv.sackW}, {"b", srv.sackB}} {
		// Save shared tomb — the cross-piece board-level memory
		if ce.deleg.SharedTomb != nil {
			sharedPath := tombChildPath(srv.saveFile, ce.side, "shared")
			writeTomb(ce.deleg.SharedTomb, sharedPath, ce.deleg.SharedPulse.CompositeResonance)
		}
		// Save each piece child's individual tomb
		for i, child := range ce.deleg.Children {
			if child == nil {
				continue
			}
			pType := pieceChildOrder[i]
			path := tombChildPath(srv.saveFile, ce.side, pType)
			writeTomb(child.Tomb, path, child.LastPulse.CompositeResonance)
		}
	}
}

func (srv *Server) loadTombs() {
	if srv.saveFile == "" {
		return
	}
	type colorEntry struct {
		side  string
		deleg *DelegatorSack
	}
	for _, ce := range []colorEntry{{"w", srv.sackW}, {"b", srv.sackB}} {
		// Load shared tomb
		sharedPath := tombChildPath(srv.saveFile, ce.side, "shared")
		if t, hdr, err := readTomb(sharedPath); err == nil && t != nil {
			if validateTomb(t, hdr) {
				ce.deleg.SharedTomb = t
			}
		}
		// Load each piece child's individual tomb
		for i, child := range ce.deleg.Children {
			if child == nil {
				continue
			}
			pType := pieceChildOrder[i]
			path := tombChildPath(srv.saveFile, ce.side, pType)
			if t, hdr, err := readTomb(path); err == nil && t != nil {
				if validateTomb(t, hdr) {
					child.Tomb = t
				}
			}
		}
	}
}

// ============================================================================
// BYTE WRITER — minimal append-only binary writer
// ============================================================================

type byteWriter struct {
	buf *[]byte
}

func (w *byteWriter) u8(v byte) {
	*w.buf = append(*w.buf, v)
}

func (w *byteWriter) u32(v uint32) {
	b := [4]byte{}
	binary.LittleEndian.PutUint32(b[:], v)
	*w.buf = append(*w.buf, b[:]...)
}

func (w *byteWriter) u64(v uint64) {
	b := [8]byte{}
	binary.LittleEndian.PutUint64(b[:], v)
	*w.buf = append(*w.buf, b[:]...)
}

// ============================================================================
// BYTE READER — minimal binary reader with error tracking
// ============================================================================

type byteReader struct {
	data []byte
	pos  int
	err  bool
}

func (r *byteReader) u8() byte {
	if r.pos+1 > len(r.data) {
		r.err = true
		return 0
	}
	v := r.data[r.pos]
	r.pos++
	return v
}

func (r *byteReader) u32() uint32 {
	if r.pos+4 > len(r.data) {
		r.err = true
		return 0
	}
	v := binary.LittleEndian.Uint32(r.data[r.pos:])
	r.pos += 4
	return v
}

func (r *byteReader) u64() uint64 {
	if r.pos+8 > len(r.data) {
		r.err = true
		return 0
	}
	v := binary.LittleEndian.Uint64(r.data[r.pos:])
	r.pos += 8
	return v
}