package hist

import (
	"math"
	"testing"
)

func TestBinEdges(t *testing.T) {
	cases := []struct {
		s    float64
		want int
	}{
		{-1, 0}, {0, 0}, {0.0005, 0}, {0.001, 0}, {0.0011, 1}, {0.002, 1}, {0.005, 2},
		{0.75, 10}, {0.7501, 11}, {1, 11}, {60, 18}, {60.0001, 19}, {3600, 19}, {math.NaN(), 0},
	}
	for _, c := range cases {
		if got := Bin(c.s); got != c.want {
			t.Errorf("Bin(%v) = %d, want %d", c.s, got, c.want)
		}
	}
	if len(Bounds()) != Bins-1 {
		t.Fatalf("bounds has %d edges, want %d", len(Bounds()), Bins-1)
	}
}

func TestQuantileInterpolates(t *testing.T) {
	var h H
	for i := 0; i < 100; i++ {
		h.Add(0.003) // bin 2: (0.002, 0.005]
	}
	p50, ok := h.Quantile(0.5)
	if !ok {
		t.Fatal("not ok")
	}
	if want := 0.002 + 0.003*0.5; math.Abs(p50-want) > 1e-9 {
		t.Errorf("p50 = %v, want %v", p50, want)
	}
	if _, ok := (H{}).Quantile(0.5); ok {
		t.Error("empty histogram answered")
	}
}

func TestQuantileSpansBins(t *testing.T) {
	var h H
	for i := 0; i < 90; i++ {
		h.Add(0.001)
	}
	for i := 0; i < 10; i++ {
		h.Add(0.4) // (0.3, 0.5]
	}
	p95, _ := h.Quantile(0.95)
	if p95 <= 0.3 || p95 > 0.5 {
		t.Errorf("p95 = %v, want inside (0.3, 0.5]", p95)
	}
	p50, _ := h.Quantile(0.5)
	if p50 > 0.001 {
		t.Errorf("p50 = %v, want within the first bin", p50)
	}
}

func TestOverflowBinAnswersLowerEdge(t *testing.T) {
	var h H
	h.Add(120)
	if v, _ := h.Quantile(0.99); v != 60 {
		t.Errorf("overflow quantile = %v, want 60", v)
	}
}

func TestMergeEncodeRoundTrip(t *testing.T) {
	var a, b H
	a.Add(0.01)
	a.Add(2.5)
	b.Add(0.01)
	b.Add(500)
	a.Merge(b)
	if a.Total() != 4 {
		t.Fatalf("total = %d", a.Total())
	}
	got, err := Decode(a.Encode())
	if err != nil || got != a {
		t.Fatalf("round trip = %v, %v", got, err)
	}
	if (H{}).Encode() != nil {
		t.Error("empty histogram should encode to nil")
	}
	if h, err := Decode(nil); err != nil || h.Total() != 0 {
		t.Errorf("Decode(nil) = %v, %v", h, err)
	}
	if _, err := Decode([]byte{1, 2, 3}); err == nil {
		t.Error("short blob accepted")
	}
	if _, err := Decode(append(a.Encode(), 9)); err == nil {
		t.Error("trailing bytes accepted")
	}
}
