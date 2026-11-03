// Package align finds where one recording sits inside another by
// cross-correlating their waveforms (GCC-PHAT), at a low sample rate over the
// whole length first and then at the playback rate around the match.
package align

import (
	"math"
	"math/bits"
	"sync"
)

// plan holds what a radix-2 FFT of one size reuses: the twiddle factors and
// the bit-reversal permutation.
type plan struct {
	n       int
	twiddle []complex128 // e^(-2πik/n) for k < n/2
	rev     []int32
}

var plans sync.Map // int → *plan

func planFor(n int) *plan {
	if p, ok := plans.Load(n); ok {
		return p.(*plan)
	}
	if n&(n-1) != 0 || n < 2 {
		panic("align: FFT size must be a power of two")
	}
	p := &plan{n: n, twiddle: make([]complex128, n/2), rev: make([]int32, n)}
	for k := range p.twiddle {
		s, c := math.Sincos(-2 * math.Pi * float64(k) / float64(n))
		p.twiddle[k] = complex(c, s)
	}
	shift := 64 - bits.Len(uint(n-1))
	for i := range p.rev {
		p.rev[i] = int32(bits.Reverse64(uint64(i)) >> shift)
	}
	actual, _ := plans.LoadOrStore(n, p)
	return actual.(*plan)
}

// nextPow2 returns the smallest power of two that is at least n.
func nextPow2(n int) int {
	if n <= 1 {
		return 1
	}
	return 1 << bits.Len(uint(n-1))
}

// fft transforms x in place; inverse runs the inverse transform, scaled by
// 1/n.
func fft(x []complex128, inverse bool) {
	n := len(x)
	p := planFor(n)
	for i, j := range p.rev {
		if int(j) > i {
			x[i], x[j] = x[j], x[i]
		}
	}
	for size := 2; size <= n; size <<= 1 {
		half := size / 2
		step := n / size
		for start := 0; start < n; start += size {
			k := 0
			for j := start; j < start+half; j++ {
				w := p.twiddle[k]
				if inverse {
					w = complex(real(w), -imag(w))
				}
				t := w * x[j+half]
				x[j+half] = x[j] - t
				x[j] += t
				k += step
			}
		}
	}
	if inverse {
		s := complex(1/float64(n), 0)
		for i := range x {
			x[i] *= s
		}
	}
}
