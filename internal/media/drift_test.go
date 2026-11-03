package media

import (
	"bytes"
	"context"
	"encoding/binary"
	"math"
	"path/filepath"
	"testing"

	"wavealign/internal/align"
	"wavealign/internal/dsp"
)

// TestExportDrift makes a camera clip whose clock runs 7.9 ppm slower than
// the recorder's, as a TASCAM against a Sony did, matches it, and checks the
// exported audio follows the camera's sound within a few microseconds from
// its first second to its last.
func TestExportDrift(t *testing.T) {
	if err := Configure(""); err != nil {
		t.Skip(err)
	}
	ctx := context.Background()
	dir := t.TempDir()
	const rate = 48000
	master := filepath.Join(dir, "rec.wav")
	ff(t, "-f", "lavfi", "-i", "anoisesrc=d=200:c=pink:r=48000:a=0.5:seed=11",
		"-af", "volume='if(gt(sin(2*PI*t*1.7)+sin(2*PI*t*0.53),0.3),1,0.05)':eval=frame", master)
	m, err := DecodeMono(ctx, master, 0, 0, rate)
	if err != nil {
		t.Fatal(err)
	}
	// The camera heard the recorder's 40th second on, its clock slow.
	const a, s = 40*rate + 0.37, 7.9e-6
	n := 150 * rate
	cam := make([]float32, n)
	one := make([]float32, 1)
	for i := range cam {
		dsp.At(m, 1, 0, a+float64(i)*(1+s), one)
		cam[i] = 0.4 * one[0]
	}
	var raw bytes.Buffer
	for _, v := range cam {
		binary.Write(&raw, binary.LittleEndian, math.Float32bits(v))
	}
	clip := filepath.Join(dir, "cam.mov")
	cmd, _ := command(ctx, false, "-v", "error", "-y", "-f", "lavfi", "-i", "testsrc2=d=150:s=320x180:r=30",
		"-f", "f32le", "-ar", "48000", "-ch_layout", "mono", "-i", "pipe:0",
		"-map", "0:v", "-map", "1:a", "-c:v", "libx264", "-preset", "ultrafast", "-c:a", "pcm_s24le", "-shortest", clip)
	cmd.Stdin = &raw
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}

	// Match: coarse at 8 kHz, then tracked at 48 kHz.
	ext8, _ := DecodeMono(ctx, master, 0, 0, AnalysisRate)
	q8, _ := DecodeMono(ctx, clip, 1, 0, AnalysisRate)
	res, err := align.Find(ext8, q8, align.Options{Rate: AnalysisRate})
	if err != nil {
		t.Fatal(err)
	}
	q, _ := DecodeMono(ctx, clip, 1, 0, rate)
	reader := func(x []float32) align.Reader {
		return func(dst []float32, f int64) {
			for i := range dst {
				k := f + int64(i)
				dst[i] = 0
				if k >= 0 && k < int64(len(x)) {
					dst[i] = x[k]
				}
			}
		}
	}
	guess := align.Line{A: res.LagAt(0) * rate, S: res.Slope}
	tr, ok := align.Track(reader(m), reader(q), int64(len(q)), rate, guess)
	if !ok {
		t.Fatal("not tracked")
	}
	t.Logf("coarse %.4f s %.2f ppm → tracked %.6f s %.3f ppm (rms %.2f frames, %d/%d)",
		res.LagAt(0), res.Slope*1e6, tr.A/rate, tr.S*1e6, tr.RMS, tr.Used, tr.Windows)

	info, _ := Probe(ctx, clip)
	residual := func(drift float64) float64 {
		out := filepath.Join(dir, "out.mov")
		err := Export(ctx, ExportOptions{Video: clip, Output: out, Inputs: []AudioInput{{master, 0}},
			Offset: tr.A / rate, Drift: drift, Duration: info.Duration,
			SampleRate: rate, Layout: "mono", Channels: 1, Codec: CodecPCM})
		if err != nil {
			t.Fatal(err)
		}
		got, _ := DecodeMono(ctx, out, 1, 0, rate)
		worst := 0.0
		for sec := 1; sec+3 < 150; sec += 7 {
			qs := sec * rate
			pos, _ := align.Locate(got[qs-rate/100:qs+2*rate+rate/100], q[qs:qs+2*rate], 0)
			worst = math.Max(worst, math.Abs(pos-rate/100))
		}
		return worst / rate * 1e6 // µs
	}
	without := residual(0)
	with := residual(tr.S)
	t.Logf("worst error along the clip: %.1f µs without drift correction, %.1f µs with", without, with)
	if with > 30 {
		t.Fatalf("with drift correction the audio is still %.1f µs off", with)
	}
	if without < 500 {
		t.Fatalf("without correction only %.1f µs off: the test does not drift", without)
	}
}
