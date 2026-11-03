package dsp

import (
	"math"
	"testing"
)

// A sine read at fractional positions comes out as the sine at those
// positions, well below audibility in error.
func TestAt(t *testing.T) {
	const rate = 48000.0
	n := 4800
	src := make([]float32, 2*n)
	f := func(x float64) float64 {
		return 0.5*math.Sin(2*math.Pi*1000*x/rate) + 0.3*math.Sin(2*math.Pi*9000*x/rate)
	}
	for i := 0; i < n; i++ {
		src[2*i] = float32(f(float64(i)))
		src[2*i+1] = -src[2*i]
	}
	out := make([]float32, 2)
	worst := 0.0
	for x := 100.0; x < float64(n-100); x += 0.737 {
		At(src, 2, 0, x, out)
		worst = math.Max(worst, math.Abs(float64(out[0])-f(x)))
		if out[1] != -out[0] {
			t.Fatal("channels mixed")
		}
	}
	if db := 20 * math.Log10(worst); db > -70 {
		t.Fatalf("error %.1f dB", db)
	}
	// Whole positions copy, and frames outside are silence.
	At(src, 2, 10, 15, out)
	if out[0] != src[10] {
		t.Fatal("a whole position did not copy its frame")
	}
	At(src, 2, 0, -50.5, out)
	if out[0] != 0 {
		t.Fatal("before the signal is not silent")
	}
}
