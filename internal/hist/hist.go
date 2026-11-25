// Package hist is the fixed 20-bin latency histogram behind the per-site p50 and
// p95. Bins are fixed so two histograms merge by adding counts, which is what
// lets a minute roll up into a quarter hour without keeping samples.
package hist

import (
	"encoding/binary"
	"errors"
	"math"
)

// Bins is the number of buckets, the last of which is unbounded.
const Bins = 20

// bounds are the upper edges in seconds: 1, 2, 5, 10, 20, 50, 100, 200, 300, 500,
// 750 ms, then 1, 1.5, 2, 3, 5, 10, 20, 60 s. The 20th bin has no upper edge.
var bounds = [Bins - 1]float64{
	0.001, 0.002, 0.005, 0.010, 0.020, 0.050, 0.100, 0.200, 0.300, 0.500, 0.750,
	1, 1.5, 2, 3, 5, 10, 20, 60,
}

// Bounds returns a copy of the finite upper edges, in seconds.
func Bounds() []float64 {
	out := make([]float64, len(bounds))
	copy(out, bounds[:])
	return out
}

// H holds one count per bin.
type H [Bins]uint32

// Bin is the index a duration in seconds falls in. A value equal to an edge goes
// in the lower bin; NaN and non-positive values count as the first bin.
func Bin(seconds float64) int {
	if math.IsNaN(seconds) || seconds <= 0 {
		return 0
	}
	for i, b := range bounds {
		if seconds <= b {
			return i
		}
	}
	return Bins - 1
}

// Add counts one observation.
func (h *H) Add(seconds float64) { h[Bin(seconds)]++ }

// Merge adds o into h.
func (h *H) Merge(o H) {
	for i := range h {
		h[i] += o[i]
	}
}

// Total is the number of observations.
func (h H) Total() uint64 {
	var n uint64
	for _, c := range h {
		n += uint64(c)
	}
	return n
}

// Quantile estimates the q-th quantile (0..1) in seconds by interpolating
// linearly inside the bin that holds it. The last, unbounded bin answers with
// its lower edge. ok is false for an empty histogram. The error is bounded by
// the bin width, about 20% for the bins above 20 ms.
func (h H) Quantile(q float64) (seconds float64, ok bool) {
	total := h.Total()
	if total == 0 {
		return 0, false
	}
	q = math.Min(math.Max(q, 0), 1)
	rank := q * float64(total)
	var cum float64
	for i, c := range h {
		if c == 0 {
			continue
		}
		next := cum + float64(c)
		if rank <= next || i == Bins-1 {
			lo := 0.0
			if i > 0 {
				lo = bounds[i-1]
			}
			if i == Bins-1 {
				return lo, true
			}
			frac := (rank - cum) / float64(c)
			return lo + (bounds[i]-lo)*frac, true
		}
		cum = next
	}
	return bounds[len(bounds)-1], true
}

// Encode packs h as 20 uvarints. An empty histogram encodes as nil so a site
// with no timing data stores no blob.
func (h H) Encode() []byte {
	if h.Total() == 0 {
		return nil
	}
	buf := make([]byte, 0, Bins*2)
	for _, c := range h {
		buf = binary.AppendUvarint(buf, uint64(c))
	}
	return buf
}

// Decode reverses Encode. A nil or empty blob is an empty histogram.
func Decode(b []byte) (H, error) {
	var h H
	if len(b) == 0 {
		return h, nil
	}
	for i := range h {
		v, n := binary.Uvarint(b)
		if n <= 0 || v > math.MaxUint32 {
			return H{}, errors.New("hist: malformed histogram blob")
		}
		h[i] = uint32(v)
		b = b[n:]
	}
	if len(b) != 0 {
		return H{}, errors.New("hist: trailing bytes in histogram blob")
	}
	return h, nil
}
