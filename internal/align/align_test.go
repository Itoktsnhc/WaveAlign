package align

import (
	"math"
	"math/cmplx"
	"math/rand"
	"testing"
)

func TestFFTRoundTrip(t *testing.T) {
	n := 1024
	x := make([]complex128, n)
	for i := range x {
		x[i] = complex(rand.Float64(), rand.Float64())
	}
	y := append([]complex128(nil), x...)
	fft(y, false)
	// Compare one bin against a direct DFT.
	k := 37
	var want complex128
	for j, v := range x {
		want += v * cmplx.Exp(complex(0, -2*math.Pi*float64(j*k)/float64(n)))
	}
	if cmplx.Abs(y[k]-want) > 1e-9 {
		t.Fatalf("bin %d = %v, want %v", k, y[k], want)
	}
	fft(y, true)
	for i := range x {
		if cmplx.Abs(y[i]-x[i]) > 1e-12 {
			t.Fatalf("round trip differs at %d", i)
		}
	}
}

// speechLike makes noise in syllable-like bursts.
func speechLike(rng *rand.Rand, n, rate int) []float32 {
	out := make([]float32, n)
	env := 0.0
	target := 0.0
	for i := range out {
		if i%(rate/8) == 0 {
			if rng.Float64() < 0.6 {
				target = 0.2 + rng.Float64()
			} else {
				target = 0.01
			}
		}
		env += (target - env) * 0.002
		out[i] = float32(env * rng.NormFloat64())
	}
	return out
}

// otherMic filters, scales and adds noise, as a second microphone would.
func otherMic(rng *rand.Rand, x []float32) []float32 {
	out := make([]float32, len(x))
	prev := float32(0)
	for i, v := range x {
		prev = 0.7*prev + 0.3*v // low-pass
		out[i] = 0.4*prev + float32(0.02*rng.NormFloat64())
	}
	return out
}

func TestFindOffset(t *testing.T) {
	const rate = 8000
	rng := rand.New(rand.NewSource(1))
	ref := speechLike(rng, 300*rate, rate)
	start := 987654
	q := otherMic(rng, ref[start:start+90*rate])
	res, err := Find(ref, q, Options{Rate: rate})
	if err != nil {
		t.Fatal(err)
	}
	want := float64(start) / rate
	if math.Abs(res.Lag-want) > 0.0005 {
		t.Fatalf("lag %.5f s, want %.5f (conf %.2f, chunks %+v)", res.Lag, want, res.Confidence, res.Chunks)
	}
	if res.Confidence < 0.5 {
		t.Errorf("confidence %.2f", res.Confidence)
	}
	t.Logf("lag %.6f conf %.2f slope %.2g", res.Lag, res.Confidence, res.Slope)
}

func TestFindVideoStartsBeforeAudio(t *testing.T) {
	const rate = 8000
	rng := rand.New(rand.NewSource(2))
	ref := speechLike(rng, 120*rate, rate)
	// The camera ran 7 s before the recorder.
	pre := speechLike(rng, 7*rate, rate)
	q := otherMic(rng, append(pre, ref[:40*rate]...))
	res, err := Find(ref, q, Options{Rate: rate})
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(res.Lag-(-7)) > 0.0005 {
		t.Fatalf("lag %.5f, want -7 (chunks %+v)", res.Lag, res.Chunks)
	}
}

func TestFindDrift(t *testing.T) {
	const rate = 8000
	rng := rand.New(rand.NewSource(3))
	ref := speechLike(rng, 700*rate, rate)
	// The query's clock runs 40 ppm slow: resample by linear interpolation.
	const ppm = 40e-6
	start := 50.0 * rate
	n := 600 * rate
	src := make([]float32, n)
	for i := range src {
		p := start + float64(i)*(1+ppm)
		j := int(p)
		f := float32(p - float64(j))
		src[i] = ref[j]*(1-f) + ref[j+1]*f
	}
	q := otherMic(rng, src)
	res, err := Find(ref, q, Options{Rate: rate})
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(res.Slope-ppm) > 3e-6 {
		t.Fatalf("slope %.2f ppm, want 40 (chunks %+v)", res.Slope*1e6, res.Chunks)
	}
	wantMid := (start + float64(n)/2*(1+ppm) - float64(n)/2) / rate
	if math.Abs(res.Lag-wantMid) > 0.001 {
		t.Fatalf("lag at middle %.5f, want %.5f", res.Lag, wantMid)
	}
	t.Logf("slope %.2f ppm, lag %.5f", res.Slope*1e6, res.Lag)
}

func TestLocate(t *testing.T) {
	const rate = 48000
	rng := rand.New(rand.NewSource(4))
	ref := speechLike(rng, 6*rate, rate)
	chunk := otherMic(rng, ref[rate+1234:rate+1234+4*rate])
	pos, conf := Locate(ref[rate:rate+4*rate+3000], chunk, 0)
	if math.Abs(pos-1234) > 0.5 {
		t.Fatalf("pos %.2f, want 1234 (conf %.2f)", pos, conf)
	}
}
