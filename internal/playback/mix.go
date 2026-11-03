// Package playback mixes the external audio and the videos' own sound into
// one stream, so that both play sample-locked for comparing them.
package playback

import (
	"io"
	"sync"

	"wavealign/internal/media"
)

// Rate is the rate the mix plays at.
const Rate = media.PlayRate

// Mode says what the mix lets through.
type Mode int

const (
	External Mode = iota // the external audio
	Original             // the videos' own sound
	Both                 // both, mixed
	Split                // the external audio left, the videos' sound right
)

// Segment is decoded audio placed on the mix's timeline.
type Segment struct {
	PCM   *media.PCM
	Start int64 // the mix frame its first frame plays at
}

// Params are the mix's settings, changed as it plays.
type Params struct {
	Mode         Mode
	GainA, GainB float32 // external, original
}

// Mix is an audio.Decoder over the external audio (A) and the videos' sound
// (B). Its methods are safe from any goroutine.
type Mix struct {
	mu     sync.Mutex
	a, b   []Segment
	frames int64
	pos    int64
	p      Params
	ta, tb []float32
}

// NewMix makes a mix of frames frames.
func NewMix(a, b []Segment, frames int64, p Params) *Mix {
	return &Mix{a: a, b: b, frames: frames, p: p}
}

// SetParams changes what is heard from the next frames read.
func (m *Mix) SetParams(p Params) {
	m.mu.Lock()
	m.p = p
	m.mu.Unlock()
}

// SetB places the videos' sound anew, as a clip moves.
func (m *Mix) SetB(b []Segment) {
	m.mu.Lock()
	m.b = b
	m.mu.Unlock()
}

func (m *Mix) SampleRate() int { return Rate }
func (m *Mix) Channels() int   { return 2 }

func (m *Mix) Len() int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.frames
}

func (m *Mix) SetPosition(frame int64) error {
	m.mu.Lock()
	m.pos = max(0, min(frame, m.frames))
	m.mu.Unlock()
	return nil
}

func (m *Mix) Read(buf []float32) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := min(int64(len(buf)/2), m.frames-m.pos)
	if n <= 0 {
		return 0, io.EOF
	}
	out := buf[:2*n]
	clear(out)
	if cap(m.ta) < len(out) {
		m.ta = make([]float32, len(out))
		m.tb = make([]float32, len(out))
	}
	ta, tb := m.ta[:len(out)], m.tb[:len(out)]
	hasA := m.p.Mode != Original && sum(m.a, m.pos, n, ta)
	hasB := m.p.Mode != External && sum(m.b, m.pos, n, tb)
	ga, gb := m.p.GainA, m.p.GainB
	for i := 0; i < len(out); i += 2 {
		var l, r float32
		switch m.p.Mode {
		case Split:
			if hasA {
				l = ga * (ta[i] + ta[i+1]) / 2
			}
			if hasB {
				r = gb * (tb[i] + tb[i+1]) / 2
			}
		default:
			if hasA {
				l += ga * ta[i]
				r += ga * ta[i+1]
			}
			if hasB {
				l += gb * tb[i]
				r += gb * tb[i+1]
			}
		}
		out[i], out[i+1] = clip(l), clip(r)
	}
	m.pos += n
	return len(out), nil
}

// sum adds into dst the segments' frames [pos, pos+n), reporting whether any
// was there.
func sum(segs []Segment, pos, n int64, dst []float32) bool {
	clear(dst)
	any := false
	var tmp []float32
	for _, s := range segs {
		from, to := max(pos, s.Start), min(pos+n, s.Start+s.PCM.Frames)
		if to <= from {
			continue
		}
		k := to - from
		if cap(tmp) < int(2*k) {
			tmp = make([]float32, 2*k)
		}
		t := tmp[:2*k]
		s.PCM.Read(t, from-s.Start)
		off := 2 * (from - pos)
		for i, v := range t {
			dst[off+int64(i)] += v
		}
		any = true
	}
	return any
}

func clip(v float32) float32 { return max(-1, min(1, v)) }
