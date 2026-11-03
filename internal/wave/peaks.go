// Package wave keeps the peaks of long recordings at several resolutions, so
// a timeline draws their waveform at any zoom without reading the samples.
package wave

import (
	"encoding/binary"
	"io"
	"math"
)

// BucketFrames is how many frames the finest level sums up.
const BucketFrames = 32

// Peaks are the minimum and maximum of a recording over buckets of frames,
// at levels whose buckets double in size.
type Peaks struct {
	Rate   int   // frames per second
	Frames int64 // frames in the recording
	// Levels[i] holds min, max pairs for buckets of BucketFrames<<i
	// frames, in [-32768, 32767].
	Levels [][]int16
	// Max is the largest absolute value, to normalize the drawing.
	Max int16
}

// Builder makes Peaks from interleaved 16-bit samples as they come.
type Builder struct {
	channels int
	rate     int
	level0   []int16
	lo, hi   int16
	n        int
	frames   int64
	max      int16
}

func NewBuilder(rate, channels int) *Builder {
	return &Builder{rate: rate, channels: channels, lo: math.MaxInt16, hi: math.MinInt16}
}

// Add takes interleaved samples, a whole number of frames.
func (b *Builder) Add(samples []int16) {
	for i := 0; i+b.channels <= len(samples); i += b.channels {
		for _, v := range samples[i : i+b.channels] {
			b.lo = min(b.lo, v)
			b.hi = max(b.hi, v)
		}
		b.n++
		b.frames++
		if b.n == BucketFrames {
			b.flush()
		}
	}
}

func (b *Builder) flush() {
	if b.n == 0 {
		return
	}
	b.level0 = append(b.level0, b.lo, b.hi)
	b.max = max(b.max, absI16(b.lo), absI16(b.hi))
	b.lo, b.hi, b.n = math.MaxInt16, math.MinInt16, 0
}

// Finish returns the peaks of what was added.
func (b *Builder) Finish() *Peaks {
	b.flush()
	return build(b.rate, b.frames, b.level0, b.max)
}

func build(rate int, frames int64, level0 []int16, mx int16) *Peaks {
	p := &Peaks{Rate: rate, Frames: frames, Levels: [][]int16{level0}, Max: mx}
	for cur := level0; len(cur) > 2; {
		next := make([]int16, 0, len(cur)/2+2)
		for i := 0; i < len(cur); i += 4 {
			lo, hi := cur[i], cur[i+1]
			if i+3 < len(cur) {
				lo, hi = min(lo, cur[i+2]), max(hi, cur[i+3])
			}
			next = append(next, lo, hi)
		}
		p.Levels = append(p.Levels, next)
		cur = next
	}
	return p
}

// Range returns the lowest and highest sample, in [-1, 1], over frames
// [from, to), from the coarsest level whose buckets are no wider than the
// range; ok is false where the range holds no frame.
func (p *Peaks) Range(from, to float64) (lo, hi float32, ok bool) {
	if p == nil || to <= 0 || from >= float64(p.Frames) || to <= from {
		return 0, 0, false
	}
	from = max(from, 0)
	to = min(to, float64(p.Frames))
	span := to - from
	level := 0
	for level+1 < len(p.Levels) && float64(int64(BucketFrames)<<(level+1)) <= span {
		level++
	}
	size := float64(int64(BucketFrames) << level)
	data := p.Levels[level]
	i0 := int(from / size)
	i1 := max(int(math.Ceil(to/size)), i0+1)
	l, h := int16(math.MaxInt16), int16(math.MinInt16)
	for i := i0; i < i1 && 2*i+1 < len(data); i++ {
		l = min(l, data[2*i])
		h = max(h, data[2*i+1])
	}
	if l > h {
		return 0, 0, false
	}
	return float32(l) / 32768, float32(h) / 32768, true
}

// Scale returns the gain that brings the loudest peak near full scale.
func (p *Peaks) Scale() float32 {
	if p == nil || p.Max < 64 {
		return 1
	}
	return min(0.95*32768/float32(p.Max), 50)
}

// Write stores the peaks' finest level, which Read builds the rest from.
func (p *Peaks) Write(w io.Writer) error {
	hdr := []int64{int64(p.Rate), p.Frames, int64(p.Max), int64(len(p.Levels[0]))}
	if err := binary.Write(w, binary.LittleEndian, hdr); err != nil {
		return err
	}
	return binary.Write(w, binary.LittleEndian, p.Levels[0])
}

// Read loads peaks that Write stored.
func Read(r io.Reader) (*Peaks, error) {
	hdr := make([]int64, 4)
	if err := binary.Read(r, binary.LittleEndian, hdr); err != nil {
		return nil, err
	}
	if hdr[3] < 0 || hdr[3] > 1<<32 {
		return nil, io.ErrUnexpectedEOF
	}
	level0 := make([]int16, hdr[3])
	if err := binary.Read(r, binary.LittleEndian, level0); err != nil {
		return nil, err
	}
	return build(int(hdr[0]), hdr[1], level0, int16(hdr[2])), nil
}

func absI16(v int16) int16 {
	if v < 0 {
		if v == math.MinInt16 {
			return math.MaxInt16
		}
		return -v
	}
	return v
}
