package align

import (
	"errors"
	"math"
	"runtime"
	"slices"
	"sort"
	"sync"
)

// Options tune Find.
type Options struct {
	// Rate is the sample rate of both signals.
	Rate int
	// ChunkSeconds is the length of the excerpts of the query searched for
	// in the reference; 15 when zero.
	ChunkSeconds float64
	// MaxChunks bounds how many excerpts are searched; 6 when zero.
	MaxChunks int
	// Beta is the PHAT weighting exponent: 1 whitens the cross spectrum
	// fully, 0 is plain cross-correlation. 0.8 when zero.
	Beta float64
	// Progress, when set, is called with the fraction done.
	Progress func(float64)
}

func (o *Options) defaults() {
	if o.ChunkSeconds <= 0 {
		o.ChunkSeconds = 15
	}
	if o.MaxChunks <= 0 {
		o.MaxChunks = 6
	}
	if o.Beta <= 0 {
		o.Beta = 0.8
	}
}

// Chunk is one excerpt of the query and where it matched best.
type Chunk struct {
	Start, Len int     // in the query, in samples
	Lag        float64 // reference position minus query position, in samples
	Peak       float64 // the correlation peak
	Second     float64 // the best peak elsewhere
	Conf       float64 // 1 - Second/Peak
	Inlier     bool    // agrees with the chosen alignment
}

// Result is where the query sits in the reference.
type Result struct {
	// Lag is the reference time minus the query time, in seconds, at the
	// middle of the query.
	Lag float64
	// Slope is how much faster the reference runs than the query: its
	// lag grows by Slope seconds per second of query (clock drift).
	Slope float64
	// Confidence, from 0 to 1, says how clearly the match stands out and
	// how many excerpts agree on it.
	Confidence float64
	Chunks     []Chunk
	// Best is the index of the inlying chunk nearest the middle, which
	// Refine-style searches start from.
	Best int
	// Mid is the query time, in seconds, that Lag is measured at.
	Mid float64
}

// LagAt returns the lag, in seconds, at query time t.
func (r Result) LagAt(t float64) float64 { return r.Lag + r.Slope*(t-r.Mid) }

var ErrTooShort = errors.New("音频太短，无法匹配（至少需要 2 秒）")

// Find locates query q inside reference ref, both mono at o.Rate.
func Find(ref, q []float32, o Options) (Result, error) {
	o.defaults()
	rate := o.Rate
	if len(q) < 2*rate || len(ref) < 2*rate {
		return Result{}, ErrTooShort
	}
	r := emphasize(ref)
	x := emphasize(q)

	chunkLen := int(o.ChunkSeconds * float64(rate))
	var chunks []Chunk
	if len(x) <= 2*chunkLen {
		// Short: the whole query is one excerpt.
		chunks = []Chunk{{Start: 0, Len: len(x)}}
	} else {
		k := min(max(len(x)/(60*rate)+2, 2), o.MaxChunks)
		chunks = pickChunks(x, rate, chunkLen, k)
	}
	l := 0
	for _, c := range chunks {
		l = max(l, c.Len)
	}

	// The reference, padded with l zeros on both sides so that excerpts
	// overlapping its ends still match, is cut into blocks of n samples
	// that overlap by l, each correlated with every excerpt.
	n := nextPow2(4 * l)
	hop := n - l
	padded := len(r) + 2*l
	blocks := (padded - l + hop - 1) / hop
	excl := rate / 100 // 10 ms between distinct peaks

	// The excerpts' spectra, conjugated.
	specs := make([][]complex128, len(chunks))
	for i, c := range chunks {
		s := make([]complex128, n)
		for j := 0; j < c.Len; j++ {
			s[j] = complex(x[c.Start+j], 0)
		}
		fft(s, false)
		for j := range s {
			s[j] = complex(real(s[j]), -imag(s[j]))
		}
		specs[i] = s
	}

	found := make([][][]cand, blocks) // [block][chunk]
	var wg sync.WaitGroup
	var mu sync.Mutex
	done := 0
	work := make(chan int)
	for w := 0; w < max(runtime.GOMAXPROCS(0)-1, 1); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			seg := make([]complex128, n)
			prod := make([]complex128, n)
			for b := range work {
				start := b * hop
				for j := range seg {
					idx := start + j - l
					v := 0.0
					if idx >= 0 && idx < len(r) {
						v = r[idx]
					}
					seg[j] = complex(v, 0)
				}
				fft(seg, false)
				per := make([][]cand, len(chunks))
				for ci := range chunks {
					phat(prod, seg, specs[ci], o.Beta)
					fft(prod, true)
					// Valid lags: 0 ≤ k < hop (the excerpt fits without wrapping).
					var top []cand
					for range 3 {
						best, bi := -1.0, -1
						for k := 0; k < hop; k++ {
							v := math.Abs(real(prod[k]))
							if v <= best || near(top, start+k, excl) {
								continue
							}
							best, bi = v, k
						}
						if bi < 0 {
							break
						}
						c := cand{pos: start + bi, val: best}
						if bi > 0 {
							c.prev = math.Abs(real(prod[bi-1]))
						}
						if bi+1 < n {
							c.nx = math.Abs(real(prod[bi+1]))
						}
						top = append(top, c)
					}
					per[ci] = top
				}
				found[b] = per
				if o.Progress != nil {
					mu.Lock()
					done++
					o.Progress(float64(done) / float64(blocks))
					mu.Unlock()
				}
			}
		}()
	}
	for b := 0; b < blocks; b++ {
		work <- b
	}
	close(work)
	wg.Wait()

	for ci := range chunks {
		var all []cand
		for b := range found {
			all = append(all, found[b][ci]...)
		}
		sort.Slice(all, func(i, j int) bool { return all[i].val > all[j].val })
		if len(all) == 0 || all[0].val == 0 {
			continue
		}
		best := all[0]
		second := 0.0
		for _, c := range all[1:] {
			if abs(c.pos-best.pos) > excl {
				second = c.val
				break
			}
		}
		pos := float64(best.pos) + parabolic(best.prev, best.val, best.nx)
		c := &chunks[ci]
		c.Lag = pos - float64(l) - float64(c.Start)
		c.Peak, c.Second = best.val, second
		c.Conf = clamp01(1 - second/best.val)
	}

	a, b := consensus(chunks, rate)
	mid := float64(len(q)) / 2
	res := Result{
		Lag:    (a + b*mid) / float64(rate),
		Slope:  b,
		Mid:    mid / float64(rate),
		Chunks: chunks,
	}
	sum, bestDist := 0.0, math.Inf(1)
	for i, c := range chunks {
		if !c.Inlier {
			continue
		}
		sum += c.Conf
		if d := math.Abs(float64(c.Start+c.Len/2) - mid); d < bestDist {
			bestDist, res.Best = d, i
		}
	}
	res.Confidence = clamp01(sum / float64(len(chunks)))
	return res, nil
}

// consensus picks the line lag = a + b·t (in samples, t the query sample)
// that most excerpts agree with, allowing for clock drift, fits it to them and
// marks them inliers.
func consensus(chunks []Chunk, rate int) (a, b float64) {
	tol := 0.010 * float64(rate)
	const maxSlope = 500e-6
	center := func(c Chunk) float64 { return float64(c.Start) + float64(c.Len)/2 }
	bestScore, bestA, bestB := -1.0, 0.0, 0.0
	try := func(a, b, t0 float64) {
		score := 0.0
		for _, c := range chunks {
			if math.Abs(c.Lag-(a+b*(center(c)-t0))) <= tol {
				score += c.Conf + 1e-3
			}
		}
		if score > bestScore {
			bestScore, bestA, bestB = score, a-b*t0, b
		}
	}
	for i, ci := range chunks {
		try(ci.Lag, 0, center(ci))
		for _, cj := range chunks[i+1:] {
			dt := center(cj) - center(ci)
			if dt == 0 {
				continue
			}
			b := (cj.Lag - ci.Lag) / dt
			if math.Abs(b) <= maxSlope {
				try(ci.Lag, b, center(ci))
			}
		}
	}
	// Least squares over the inliers, weighted by their confidence.
	var sw, st, sl, stt, stl float64
	inliers := 0
	for i := range chunks {
		c := &chunks[i]
		t := center(*c)
		c.Inlier = math.Abs(c.Lag-(bestA+bestB*t)) <= tol
		if !c.Inlier {
			continue
		}
		inliers++
		w := c.Conf + 1e-3
		sw += w
		st += w * t
		sl += w * c.Lag
		stt += w * t * t
		stl += w * t * c.Lag
	}
	a, b = bestA, bestB
	if den := sw*stt - st*st; inliers >= 2 && den > 1e-9*sw*stt {
		b = (sw*stl - st*sl) / den
		a = (sl - b*st) / sw
	} else if inliers >= 1 {
		a, b = sl/sw, 0
	}
	return a, b
}

// Locate finds where chunk fits best inside seg, which is longer, and returns
// that position in samples (with a fraction) and how clearly it stands out.
func Locate(seg, chunk []float32, beta float64) (float64, float64) {
	pos, conf, _ := LocateSigned(seg, chunk, beta)
	return pos, conf
}

// LocateSigned is Locate that also tells whether chunk matches seg with its
// polarity inverted, as two microphones wired the other way round.
func LocateSigned(seg, chunk []float32, beta float64) (pos, conf float64, inverted bool) {
	if len(chunk) == 0 || len(seg) < len(chunk) {
		return 0, 0, false
	}
	if beta <= 0 {
		beta = 0.8
	}
	s := emphasize(seg)
	c := emphasize(chunk)
	n := nextPow2(len(s) + len(c))
	a := make([]complex128, n)
	b := make([]complex128, n)
	for i, v := range s {
		a[i] = complex(v, 0)
	}
	for i, v := range c {
		b[i] = complex(v, 0)
	}
	fft(a, false)
	fft(b, false)
	for i := range b {
		b[i] = complex(real(b[i]), -imag(b[i]))
	}
	out := make([]complex128, n)
	phat(out, a, b, beta)
	fft(out, true)
	last := len(s) - len(c)
	best, bi := -1.0, 0
	for k := 0; k <= last; k++ {
		if v := math.Abs(real(out[k])); v > best {
			best, bi = v, k
		}
	}
	excl := max(len(c)/200, 4)
	second := 0.0
	for k := 0; k <= last; k++ {
		if abs(k-bi) > excl {
			second = max(second, math.Abs(real(out[k])))
		}
	}
	prev, next := 0.0, 0.0
	if bi > 0 {
		prev = math.Abs(real(out[bi-1]))
	}
	if bi < last {
		next = math.Abs(real(out[bi+1]))
	}
	if best > 0 {
		conf = clamp01(1 - second/best)
	}
	return float64(bi) + parabolic(prev, best, next), conf, real(out[bi]) < 0
}

// phat writes into dst the cross spectrum a·conj(b) (b is given conjugated)
// weighted by 1/|a·b|^beta.
func phat(dst, a, bConj []complex128, beta float64) {
	mean := 0.0
	for i := range dst {
		p := a[i] * bConj[i]
		dst[i] = p
		mean += math.Hypot(real(p), imag(p))
	}
	mean /= float64(len(dst))
	eps := 1e-3*mean + 1e-30
	for i, p := range dst {
		m := math.Hypot(real(p), imag(p))
		dst[i] = p / complex(math.Pow(m+eps, beta), 0)
	}
}

// emphasize converts to float64, removes the mean and lifts the highs
// (pre-emphasis), which sharpens the correlation of recordings whose low end
// differs from microphone to microphone.
func emphasize(x []float32) []float64 {
	out := make([]float64, len(x))
	mean := 0.0
	for _, v := range x {
		mean += float64(v)
	}
	if len(x) > 0 {
		mean /= float64(len(x))
	}
	prev := 0.0
	for i, v := range x {
		cur := float64(v) - mean
		out[i] = cur - 0.95*prev
		prev = cur
	}
	return out
}

// pickChunks chooses k excerpts of length l from x, one in each of k equal
// regions, where the level varies most: speech, music and hits rather than
// silence or steady noise.
func pickChunks(x []float64, rate, l, k int) []Chunk {
	frame := rate / 20 // 50 ms
	nf := len(x) / frame
	logE := make([]float64, nf)
	for i := range logE {
		e := 0.0
		for _, v := range x[i*frame : (i+1)*frame] {
			e += v * v
		}
		logE[i] = math.Log10(e/float64(frame) + 1e-10)
	}
	lf := l / frame
	region := len(x) / k
	var out []Chunk
	for ri := 0; ri < k; ri++ {
		lo := ri * region / frame
		hi := min((ri+1)*region/frame, nf) - lf
		if hi < lo {
			hi = lo
		}
		bestScore, best := math.Inf(-1), lo
		for s := lo; s <= hi; s += max(lf/8, 1) {
			end := min(s+lf, nf)
			if end-s < 2 {
				break
			}
			score := variance(logE[s:end]) + 0.1*meanOf(logE[s:end])
			if score > bestScore {
				bestScore, best = score, s
			}
		}
		start := min(best*frame, len(x)-l)
		out = append(out, Chunk{Start: max(start, 0), Len: min(l, len(x))})
	}
	slices.SortFunc(out, func(a, b Chunk) int { return a.Start - b.Start })
	return slices.CompactFunc(out, func(a, b Chunk) bool { return a.Start == b.Start })
}

// cand is a correlation peak of one block.
type cand struct {
	pos      int // in padded reference coordinates
	val      float64
	prev, nx float64 // neighbors, for interpolation
}

// near reports whether k is within excl of a peak already taken.
func near(top []cand, k, excl int) bool {
	for _, c := range top {
		if abs(c.pos-k) <= excl {
			return true
		}
	}
	return false
}

func variance(v []float64) float64 {
	m := meanOf(v)
	s := 0.0
	for _, x := range v {
		s += (x - m) * (x - m)
	}
	return s / float64(len(v))
}

func meanOf(v []float64) float64 {
	s := 0.0
	for _, x := range v {
		s += x
	}
	return s / float64(len(v))
}

// parabolic returns the offset, within ±0.5, of the vertex of the parabola
// through three equally spaced values around a peak.
func parabolic(a, b, c float64) float64 {
	den := a - 2*b + c
	if den == 0 {
		return 0
	}
	d := 0.5 * (a - c) / den
	return math.Max(-0.5, math.Min(0.5, d))
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func clamp01(v float64) float64 { return math.Max(0, math.Min(1, v)) }
