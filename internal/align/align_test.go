package align

import (
	"math"
	"math/cmplx"
	"math/rand"
	"testing"

	"wavealign/internal/dsp"
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

// TestTrack finds offset and drift to a fraction of a frame at 48 kHz, as a
// recorder whose clock runs 7.9 ppm fast against a camera's.
func TestTrack(t *testing.T) {
	const rate = 48000
	rng := rand.New(rand.NewSource(5))
	ref := speechLike(rng, 400*rate, rate)
	const a, s = 3_000_123.37, 7.9e-6 // ref frame = q frame + a + s·q frame
	n := 250 * rate
	q := make([]float32, n)
	out := make([]float32, 1)
	for i := range q {
		dsp.At(ref, 1, 0, float64(i)+a+s*float64(i), out)
		q[i] = out[0]
	}
	q = otherMic(rng, q)
	reader := func(x []float32) Reader {
		return func(dst []float32, frame int64) {
			for i := range dst {
				k := frame + int64(i)
				dst[i] = 0
				if k >= 0 && k < int64(len(x)) {
					dst[i] = x[k]
				}
			}
		}
	}
	// From a guess 1.5 ms off and without the drift, as the coarse match gives.
	got, ok := Track(reader(ref), reader(q), int64(n), rate, Line{A: a + 72})
	if !ok {
		t.Fatal("not tracked")
	}
	if got.Inverted {
		t.Fatal("found inverted")
	}
	// The same microphone wired the other way round.
	inv := make([]float32, len(q))
	for i, v := range q {
		inv[i] = -v
	}
	if gi, ok := Track(reader(ref), reader(inv), int64(n), rate, Line{A: a + 72}); !ok || !gi.Inverted || math.Abs(gi.A-got.A) > 0.01 {
		t.Fatalf("inverted: ok %v inverted %v a %.3f", ok, gi.Inverted, gi.A)
	}
	t.Logf("a %.3f (want %.3f), drift %.3f ppm, rms %.3f frames, %d/%d excerpts", got.A, a, got.S*1e6, got.RMS, got.Used, got.Windows)
	for _, at := range []float64{0, float64(n) / 2, float64(n)} {
		if d := got.Lag(at) - (a + s*at); math.Abs(d) > 0.1*rate/1000 {
			t.Fatalf("off by %.3f ms at frame %.0f", d/rate*1000, at)
		}
	}
}
