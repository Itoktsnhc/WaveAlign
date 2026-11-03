package main

import (
	"fmt"
	"math"
)

// fmtTime writes seconds as [-][h:]mm:ss.mmm.
func fmtTime(t float64) string {
	sign := ""
	if t < 0 {
		sign, t = "-", -t
	}
	ms := int64(math.Round(t * 1000))
	h, m, s, f := ms/3600000, ms/60000%60, ms/1000%60, ms%1000
	if h > 0 {
		return fmt.Sprintf("%s%d:%02d:%02d.%03d", sign, h, m, s, f)
	}
	return fmt.Sprintf("%s%02d:%02d.%03d", sign, m, s, f)
}

// fmtTick writes a ruler label, as precise as the step between labels.
func fmtTick(t, step float64) string {
	sign := ""
	if t < 0 {
		sign, t = "-", -t
	}
	ms := int64(math.Round(t * 1000))
	h, m, s, f := ms/3600000, ms/60000%60, ms/1000%60, ms%1000
	var tail string
	switch {
	case step < 0.01:
		tail = fmt.Sprintf(".%03d", f)
	case step < 0.1:
		tail = fmt.Sprintf(".%02d", f/10)
	case step < 1:
		tail = fmt.Sprintf(".%d", f/100)
	}
	if h > 0 {
		return fmt.Sprintf("%s%d:%02d:%02d%s", sign, h, m, s, tail)
	}
	return fmt.Sprintf("%s%02d:%02d%s", sign, m, s, tail)
}

// fmtDelta writes a small offset change as ±n ms.
func fmtDelta(d float64) string {
	return fmt.Sprintf("%+.1f ms", d*1000)
}
