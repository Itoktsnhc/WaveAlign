// Package dsp resamples audio by any ratio, with a windowed-sinc kernel, to
// take one recorder's clock to another's.
package dsp

import "math"

// Taps is the kernel's half width, in frames: each output frame sums 2·Taps
// input frames.
const Taps = 16

const phases = 1024

// kernel holds the windowed sinc at phases+1 fractional offsets, 2·Taps
// weights each, each row summing to one.
var kernel = func() []float32 {
	const cutoff = 0.97 // of Nyquist, a little below, against aliasing
	beta := 8.6         // Kaiser window: about 90 dB of stopband
	i0b := besselI0(beta)
	k := make([]float32, (phases+1)*2*Taps)
	for p := 0; p <= phases; p++ {
		frac := float64(p) / phases
		row := k[p*2*Taps : (p+1)*2*Taps]
		sum := 0.0
		w := make([]float64, 2*Taps)
		for j := range w {
			// Input frame i0 + j - Taps + 1, at distance d from x = i0 + frac.
			d := float64(j-Taps+1) - frac
			s := cutoff
			if d != 0 {
				s = math.Sin(math.Pi*cutoff*d) / (math.Pi * d)
			}
			r := d / Taps
			win := 0.0
			if r > -1 && r < 1 {
				win = besselI0(beta*math.Sqrt(1-r*r)) / i0b
			}
			w[j] = s * win
			sum += w[j]
		}
		for j := range w {
			row[j] = float32(w[j] / sum)
		}
	}
	return k
}()

func besselI0(x float64) float64 {
	sum, term := 1.0, 1.0
	for k := 1; k < 50; k++ {
		term *= (x / (2 * float64(k))) * (x / (2 * float64(k)))
		sum += term
		if term < 1e-12*sum {
			break
		}
	}
	return sum
}

// At writes into out, c channels, the signal at fractional frame x, from src:
// c-channel interleaved frames of which src[0] is frame first. Frames outside
// src count as silence. A whole x copies its frame as it is.
func At(src []float32, c int, first int64, x float64, out []float32) {
	i0 := int64(math.Floor(x))
	frac := x - float64(i0)
	n := int64(len(src) / c)
	if frac < 1e-9 {
		clear(out[:c])
		if k := i0 - first; k >= 0 && k < n {
			copy(out[:c], src[k*int64(c):(k+1)*int64(c)])
		}
		return
	}
	ph := frac * phases
	p := int(ph)
	t := float32(ph - float64(p))
	r0 := kernel[p*2*Taps : (p+1)*2*Taps]
	r1 := kernel[(p+1)*2*Taps : (p+2)*2*Taps]
	for ch := range c {
		out[ch] = 0
	}
	base := i0 - Taps + 1 - first
	for j := 0; j < 2*Taps; j++ {
		k := base + int64(j)
		if k < 0 || k >= n {
			continue
		}
		w := r0[j] + (r1[j]-r0[j])*t
		frame := src[k*int64(c) : (k+1)*int64(c)]
		for ch, v := range frame {
			out[ch] += w * v
		}
	}
}
