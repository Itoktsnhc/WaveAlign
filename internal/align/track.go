package align

import (
	"math"
	"runtime"
	"sort"
	"sync"
)

// Reader fills dst with mono samples from frame on, zeros outside the audio.
// It must be safe to call from several goroutines.
type Reader func(dst []float32, frame int64)

// Line maps the query onto the reference: query frame t sits at reference
// frame t + A + S·t, so A is the lag at the query's start and S the clock
// drift (reference frames gained per query frame).
type Line struct{ A, S float64 }

// Lag returns the lag at query frame t.
func (l Line) Lag(t float64) float64 { return l.A + l.S*t }

// Tracked is what Track measured.
type Tracked struct {
	Line
	Windows int // excerpts measured
	// Inverted says the query's polarity is the reference's turned over,
	// as most of the excerpts found.
	Inverted bool
	Used     int     // excerpts that agreed with the fit
	RMS      float64 // their distance from the fit, in frames
}

// Track measures the lag at many short excerpts along the query, at the
// rate of the readers, around the guess, and fits the line through them: the
// offset and the drift to a fraction of a frame. ok is false when too few
// excerpts are clear enough, as in a clip that is mostly silence.
func Track(ref, q Reader, frames int64, rate int, guess Line) (Tracked, bool) {
	win := int64(2 * rate)
	search := int64(rate / 50) // ±20 ms around the guess
	if frames < win+int64(rate) {
		return Tracked{Line: guess}, false
	}
	n := int64(48)
	step := max((frames-win)/n, int64(rate))
	type point struct {
		t, lag, w float64
		inv       bool
	}
	var starts []int64
	for s := int64(rate / 4); s+win <= frames; s += step {
		starts = append(starts, s)
	}
	points := make([]point, len(starts))
	var wg sync.WaitGroup
	work := make(chan int)
	for range max(runtime.GOMAXPROCS(0)-1, 1) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			chunk := make([]float32, win)
			seg := make([]float32, win+2*search)
			for i := range work {
				qs := starts[i]
				q(chunk, qs)
				if rms(chunk) < 1e-4 {
					continue // silence locates nothing
				}
				center := float64(qs) + float64(win)/2
				refStart := qs + int64(math.Round(guess.Lag(center))) - search
				ref(seg, refStart)
				pos, conf, inv := LocateSigned(seg, chunk, 0)
				if conf < 0.25 {
					continue
				}
				points[i] = point{t: center, lag: float64(refStart) + pos - float64(qs), w: conf, inv: inv}
			}
		}()
	}
	for i := range starts {
		work <- i
	}
	close(work)
	wg.Wait()

	var pts []point
	for _, p := range points {
		if p.w > 0 {
			pts = append(pts, p)
		}
	}
	res := Tracked{Line: guess, Windows: len(starts)}
	if len(pts) < 4 {
		return res, false
	}
	span := pts[len(pts)-1].t - pts[0].t
	fixSlope := span < float64(20*rate) // too short to tell the drift
	fit := func(ps []point) Line {
		var sw, st, sl, stt, stl float64
		for _, p := range ps {
			sw += p.w
			st += p.w * p.t
			sl += p.w * p.lag
			stt += p.w * p.t * p.t
			stl += p.w * p.t * p.lag
		}
		if fixSlope {
			return Line{A: (sl - guess.S*st) / sw, S: guess.S}
		}
		den := sw*stt - st*st
		s := (sw*stl - st*sl) / den
		return Line{A: (sl - s*st) / sw, S: s}
	}
	// Fit, then leave out the excerpts far from the fit (echoes, applause,
	// a cough near one microphone) and fit again, until none is left out.
	keep := pts
	line := fit(keep)
	for range 5 {
		dist := make([]float64, len(keep))
		for i, p := range keep {
			dist[i] = math.Abs(p.lag - line.Lag(p.t))
		}
		sorted := append([]float64(nil), dist...)
		sort.Float64s(sorted)
		limit := max(4*sorted[len(sorted)/2], 0.25e-3*float64(rate))
		var next []point
		for i, p := range keep {
			if dist[i] <= limit {
				next = append(next, p)
			}
		}
		if len(next) == len(keep) || len(next) < 4 {
			break
		}
		keep = next
		line = fit(keep)
	}
	ss := 0.0
	for _, p := range keep {
		d := p.lag - line.Lag(p.t)
		ss += d * d
	}
	res.Line = line
	res.Used = len(keep)
	vote := 0.0
	for _, p := range keep {
		if p.inv {
			vote -= p.w
		} else {
			vote += p.w
		}
	}
	res.Inverted = vote < 0
	res.RMS = math.Sqrt(ss / float64(len(keep)))
	return res, true
}

func rms(x []float32) float64 {
	s := 0.0
	for _, v := range x {
		s += float64(v) * float64(v)
	}
	return math.Sqrt(s / float64(len(x)))
}
