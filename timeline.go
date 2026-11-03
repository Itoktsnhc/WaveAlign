package main

import (
	"fmt"
	"math"

	"github.com/egoist/mygo/ui"

	"wavealign/internal/media"
	"wavealign/internal/wave"
)

const (
	rulerH  = 28
	laneAH  = 92
	laneVH  = 66
	laneGap = 6
)

type dragKind int

const (
	dragNone dragKind = iota
	dragSeek
	dragClip
	dragPan
)

// timeline is the view onto the timeline: which stretch shows, and what
// the pointer is dragging.
type timeline struct {
	pxPerSec   float64
	start      float64 // the time at the left edge
	scrollY    float32
	width      float32
	height     float32
	box        ui.Rect // the canvas in the window, as last laid out
	fitPending bool

	drag       dragKind
	dragClip   int
	dragX      float32
	dragOffset float64
	dragStart  float64
	moved      bool
	hoverClip  int
}

func (tl *timeline) x(t float64, r ui.Rect) float32 {
	return r.X + float32((t-tl.start)*tl.pxPerSec)
}

func (tl *timeline) t(x float32) float64 { return tl.start + float64(x)/tl.pxPerSec }

const (
	minPxPerSec = 0.005
	maxPxPerSec = 48000 / wave.BucketFrames * 2 // two DIPs a bucket
)

// fit shows lo to hi across the width.
func (tl *timeline) fit(lo, hi float64) {
	if tl.width <= 0 || hi <= lo {
		return
	}
	pad := (hi - lo) * 0.02
	tl.pxPerSec = math.Max(minPxPerSec, math.Min(maxPxPerSec, float64(tl.width)/(hi-lo+2*pad)))
	tl.start = lo - pad
}

// zoom zooms by f around x, in DIPs from the left edge.
func (tl *timeline) zoom(f float64, x float32) {
	at := tl.t(x)
	tl.pxPerSec = math.Max(minPxPerSec, math.Min(maxPxPerSec, tl.pxPerSec*f))
	tl.start = at - float64(x)/tl.pxPerSec
}

// reveal scrolls lo to hi into view, zooming out if it is wider than the view.
func (tl *timeline) reveal(lo, hi float64) {
	if tl.width <= 0 {
		return
	}
	span := float64(tl.width) / tl.pxPerSec
	if hi-lo > span*0.95 {
		tl.fit(lo, hi)
		return
	}
	if lo < tl.start || hi > tl.start+span {
		tl.start = (lo+hi)/2 - span/2
	}
}

// follow keeps the playhead in view as it plays.
func (tl *timeline) follow(t float64) {
	span := float64(tl.width) / tl.pxPerSec
	if tl.drag == dragNone && (t > tl.start+span*0.92 || t < tl.start) {
		tl.start = t - span*0.08
	}
}

func (a *app) contentHeight() float32 {
	return rulerH + laneAH + laneGap + float32(len(a.clips))*(laneVH+laneGap)
}

// clipLane returns the lane box of clip i, in the timeline's box r.
func (a *app) clipLane(i int, r ui.Rect) ui.Rect {
	y := r.Y + rulerH + laneAH + laneGap + float32(i)*(laneVH+laneGap) - a.tl.scrollY
	return ui.Rect{X: r.X, Y: y, W: r.W, H: laneVH}
}

// hitClip returns the clip under (x, y), relative to the timeline.
func (a *app) hitClip(x, y float32) *clip {
	r := ui.Rect{W: a.tl.width, H: a.tl.height}
	for i, c := range a.clips {
		lane := a.clipLane(i, r)
		if y < lane.Y || y >= lane.Y+lane.H {
			continue
		}
		x0, x1 := a.tl.x(c.offset, r), a.tl.x(c.end(), r)
		if x >= x0-2 && x <= x1+2 {
			return c
		}
	}
	return nil
}

// timelineInput takes the pointer and the wheel over the timeline.
func (a *app) timelineInput(ev ui.InputEvent) bool {
	tl := &a.tl
	switch ev.Kind {
	case ui.InputPointerDown:
		tl.moved = false
		tl.dragX = ev.X
		switch {
		case ev.Button == 2 || ev.Button == 0 && ev.Mods&ui.Alt != 0:
			tl.drag, tl.dragStart = dragPan, tl.start
		case ev.Button != 0:
			return false
		case ev.Y >= rulerH:
			if c := a.hitClip(ev.X, ev.Y); c != nil {
				a.selectClip(c.id)
				tl.drag, tl.dragClip, tl.dragOffset = dragClip, c.id, c.offset
				return true
			}
			fallthrough
		default:
			tl.drag = dragSeek
			a.seek(tl.t(ev.X))
		}
		return true
	case ui.InputPointerMove:
		if ev.Button < 0 {
			if c := a.hitClip(ev.X, ev.Y); c != nil && ev.Y >= rulerH {
				tl.hoverClip = c.id
			} else {
				tl.hoverClip = 0
			}
			return false
		}
		switch tl.drag {
		case dragSeek:
			a.seek(tl.t(ev.X))
		case dragPan:
			tl.start = tl.dragStart - float64(ev.X-tl.dragX)/tl.pxPerSec
		case dragClip:
			c := a.clipByID(tl.dragClip)
			if c == nil {
				break
			}
			dx := float64(ev.X-tl.dragX) / tl.pxPerSec
			if ev.Mods&ui.Shift != 0 {
				dx /= 10 // fine
			}
			if math.Abs(float64(ev.X-tl.dragX)) > 2 {
				tl.moved = true
			}
			if tl.moved {
				c.offset = tl.dragOffset + dx
				if a.mix != nil {
					a.mix.SetB(a.segmentsB(a.origin))
				}
			}
		default:
			return false
		}
		return true
	case ui.InputPointerUp:
		if tl.drag == dragClip && tl.moved {
			a.clipMoved()
		}
		tl.drag = dragNone
		return true
	case ui.InputScroll:
		switch {
		case ev.Mods&(ui.Ctrl|ui.Cmd) != 0:
			tl.zoom(math.Exp(-float64(ev.DY)*0.004), ev.X)
		case ev.Mods&ui.Shift != 0 && ev.DX == 0:
			maxY := max(0, a.contentHeight()-tl.height)
			tl.scrollY = max(0, min(maxY, tl.scrollY+ev.DY))
		default:
			d := ev.DX
			if d == 0 {
				d = ev.DY
			}
			tl.start += float64(d) / tl.pxPerSec
		}
		return true
	case ui.InputZoom:
		tl.zoom(float64(ev.Scale), ev.X)
		return true
	}
	return false
}

func (a *app) selectClip(id int) {
	if a.selected == id {
		return
	}
	a.selected = id
	if a.onlySel {
		a.monitorChanged()
	}
	a.prev.seeked()
}

// drawTimeline paints the ruler, the lanes and the playhead.
func (a *app) drawTimeline(p *ui.Painter, r ui.Rect) {
	t := a.theme
	tl := &a.tl
	p.Fill(r, ui.Hex("#141518"), 0)
	lo, hi := a.timeRange()
	sel := a.selectedClip()

	p.Clip(ui.Rect{X: r.X, Y: r.Y + rulerH, W: r.W, H: r.H - rulerH}, 0, func() {
		// Outside the timeline's range.
		if x := tl.x(lo, r); x > r.X {
			p.Fill(ui.Rect{X: r.X, Y: r.Y, W: x - r.X, H: r.H}, ui.Hex("#0c0d0f"), 0)
		}
		if x := tl.x(hi, r); x < r.X+r.W {
			p.Fill(ui.Rect{X: x, Y: r.Y, W: r.X + r.W - x, H: r.H}, ui.Hex("#0c0d0f"), 0)
		}

		// Lane A: the external audio, the files one after another.
		laneA := ui.Rect{X: r.X, Y: r.Y + rulerH - tl.scrollY, W: r.W, H: laneAH}
		p.Fill(laneA, ui.Hex("#1b1d22"), 0)
		for i, it := range a.audios {
			d := it.duration()
			if it.pcm != nil {
				d = it.pcm.Seconds()
			}
			x0, x1 := tl.x(it.start, r), tl.x(it.start+d, r)
			box := ui.Rect{X: x0, Y: laneA.Y + 2, W: x1 - x0, H: laneA.H - 4}
			bg := ui.Hex("#21352c")
			if i%2 == 1 {
				bg = ui.Hex("#1e3129")
			}
			p.Fill(box, bg, 4)
			if it.peaks != nil {
				drawWave(p, tl, r, it.peaks, it.peaks.Scale(), it.start, box, ui.Hex("#3ecf8e"))
			} else if it.loading {
				p.Text(x0+8, box.Y+box.H/2-7, fmt.Sprintf("解码中 %d%%", int(it.progress*100)), 11, t.TextMuted)
			}
			p.Line(x0, box.Y, x0, box.Y+box.H, 1, ui.Hex("#3ecf8e").Alpha(0.6))
			p.Clip(box, 4, func() {
				p.Text(x0+6, box.Y+3, fmt.Sprintf("%d. %s", i+1, it.name()), 11, ui.Hex("#a7f3d0"))
			})
		}
		// The selected clip's sound over the external audio, to compare.
		if sel != nil && sel.peaks != nil {
			box := ui.Rect{X: laneA.X, Y: laneA.Y + 2, W: laneA.W, H: laneA.H - 4}
			drawWave(p, tl, r, sel.peaks, sel.peaks.Scale(), sel.offset+sel.lead, box, t.Accent.Alpha(0.5))
		}
		p.Text(r.X+8, laneA.Y+laneA.H-16, "匹配源 1 · 外部音频（拼接）", 10, t.TextMuted)

		// A lane for each clip.
		for i, c := range a.clips {
			lane := a.clipLane(i, r)
			if lane.Y > r.Y+r.H || lane.Y+lane.H < r.Y {
				continue
			}
			p.Fill(lane, ui.Hex("#18191d"), 0)
			x0, x1 := tl.x(c.offset, r), tl.x(c.end(), r)
			box := ui.Rect{X: x0, Y: lane.Y + 2, W: max(x1-x0, 2), H: lane.H - 4}
			isSel := sel != nil && c.id == sel.id
			bg := ui.Hex("#232838")
			if isSel {
				bg = ui.Hex("#283252")
			} else if tl.hoverClip == c.id {
				bg = ui.Hex("#272c3d")
			}
			p.Fill(box, bg, 5)
			if c.peaks != nil {
				drawWave(p, tl, r, c.peaks, c.peaks.Scale(), c.offset+c.lead, box, ui.Hex("#7aa2ff"))
			}
			border := ui.Hex("#3a4260")
			if isSel {
				border = t.Accent
			}
			p.Stroke(box, border, 5, 1.5)
			p.Clip(box, 5, func() {
				label := c.name()
				switch {
				case c.aligning:
					label += fmt.Sprintf("   匹配中 %d%%", int(c.alignProg*100))
				case c.loading:
					label += fmt.Sprintf("   解码中 %d%%", int(c.progress*100))
				case c.aligned:
					label += fmt.Sprintf("   %s · 置信度 %d%%", fmtTime(c.offset), int(c.confidence*100+0.5))
				default:
					label += "   " + fmtTime(c.offset) + " · 未匹配"
				}
				p.Text(x0+8, box.Y+4, label, 11, ui.Hex("#dbe4ff"))
			})
		}
	})

	// The ruler, over the lanes.
	ruler := ui.Rect{X: r.X, Y: r.Y, W: r.W, H: rulerH}
	p.Fill(ruler, ui.Hex("#202227"), 0)
	p.Line(r.X, r.Y+rulerH-1, r.X+r.W, r.Y+rulerH-1, 1, t.Border)
	major, minor := tickSteps(tl.pxPerSec)
	first := math.Floor(tl.start/minor) * minor
	end := tl.t(r.W)
	for v := first; v <= end; v += minor {
		x := tl.x(v, r)
		isMajor := math.Abs(math.Remainder(v, major)) < minor*0.01
		if isMajor {
			p.Line(x, r.Y+rulerH-12, x, r.Y+rulerH, 1, t.TextMuted)
			p.Text(x+4, r.Y+4, fmtTick(v, major), 10, t.TextMuted)
		} else {
			p.Line(x, r.Y+rulerH-5, x, r.Y+rulerH, 1, t.Border)
		}
	}

	// The playhead.
	x := tl.x(a.playhead, r)
	if x >= r.X && x <= r.X+r.W {
		red := ui.Hex("#ff5d5d")
		p.Line(x, r.Y, x, r.Y+r.H, 1.5, red)
		var head ui.Path
		head.MoveTo(x-6, r.Y).LineTo(x+6, r.Y).LineTo(x, r.Y+9).Close()
		p.FillPath(&head, red)
	}
}

// drawWave paints peaks whose first frame plays at timeline time at, inside
// box, as a filled envelope.
func drawWave(p *ui.Painter, tl *timeline, r ui.Rect, pk *wave.Peaks, gain float32, at float64, box ui.Rect, col ui.Color) {
	x0 := max(box.X, r.X, tl.x(at, r))
	x1 := min(box.X+box.W, r.X+r.W, tl.x(at+float64(pk.Frames)/media.PlayRate, r))
	if x1 <= x0 {
		return
	}
	mid := box.Y + box.H/2
	amp := (box.H/2 - 3) * gain
	fpp := media.PlayRate / tl.pxPerSec // frames a DIP
	step := float32(1)
	var top, bot [][2]float32
	flush := func() {
		if len(top) > 0 {
			var path ui.Path
			path.MoveTo(top[0][0], top[0][1])
			for _, pt := range top[1:] {
				path.LineTo(pt[0], pt[1])
			}
			for i := len(bot) - 1; i >= 0; i-- {
				path.LineTo(bot[i][0], bot[i][1])
			}
			path.Close()
			p.FillPath(&path, col)
		}
		top, bot = top[:0], bot[:0]
	}
	for x := x0; x < x1; x += step {
		f := (tl.t(x-r.X) - at) * media.PlayRate
		lo, hi, ok := pk.Range(f, f+fpp*float64(step))
		if !ok {
			flush()
			continue
		}
		yh := mid - min(hi*amp, box.H/2-1)
		yl := mid - max(lo*amp, -(box.H/2-1))
		if yl-yh < 1 {
			yh, yl = mid-0.5, mid+0.5
		}
		top = append(top, [2]float32{x, yh})
		bot = append(bot, [2]float32{x, yl})
	}
	flush()
}

// tickSteps picks the ruler's major and minor steps for the zoom.
func tickSteps(pxPerSec float64) (major, minor float64) {
	steps := []float64{0.001, 0.002, 0.005, 0.01, 0.02, 0.05, 0.1, 0.2, 0.5, 1, 2, 5, 10, 15, 30, 60, 120, 300, 600, 900, 1800, 3600, 7200}
	major = steps[len(steps)-1]
	for _, s := range steps {
		if s*pxPerSec >= 90 {
			major = s
			break
		}
	}
	switch major {
	case 0.002, 0.02, 0.2, 2, 120, 7200:
		minor = major / 4
	case 30, 60, 1800, 3600:
		minor = major / 6
	case 600:
		minor = major / 10
	default:
		minor = major / 5
	}
	return major, minor
}
