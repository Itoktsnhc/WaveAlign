package main

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/egoist/mygo/ui"

	"wavealign/internal/media"
	"wavealign/internal/wave"
)

// The timeline has two lanes: the external audio (match source 1) above,
// the videos placed on it (match source 2) below, and a scroll bar.
const (
	rulerH   = 28
	laneGap  = 6
	scrollH  = 14
	minLaneH = 48
)

type dragKind int

const (
	dragNone dragKind = iota
	dragSeek
	dragClip
	dragPan
	dragScroll
)

// timeline is the view onto the timeline: which stretch shows, and what
// the pointer is dragging. Time never shows below zero.
type timeline struct {
	pxPerSec   float64
	start      float64 // the time at the left edge, never negative
	end        float64 // the end of the content, as last laid out
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

// span is how many seconds the width shows.
func (tl *timeline) span() float64 { return float64(tl.width) / tl.pxPerSec }

// maxPxPerSec zooms in to two DIPs a bucket of peaks.
const maxPxPerSec = 48000 / wave.BucketFrames * 2

// minPxPerSec zooms out no further than the whole content.
func (tl *timeline) minPxPerSec() float64 {
	if tl.width <= 0 || tl.end <= 0 {
		return 0.001
	}
	return float64(tl.width) / tl.end
}

// clamp keeps the zoom and the view within the content: from zero, and not
// past its end by more than half a view.
func (tl *timeline) clamp() {
	tl.pxPerSec = math.Max(tl.minPxPerSec(), math.Min(maxPxPerSec, tl.pxPerSec))
	if tl.width > 0 {
		tl.start = math.Min(tl.start, math.Max(0, tl.end-tl.span()*0.5))
	}
	tl.start = math.Max(0, tl.start)
}

// fit shows the whole content.
func (tl *timeline) fit() {
	if tl.width <= 0 || tl.end <= 0 {
		return
	}
	tl.pxPerSec = float64(tl.width) / tl.end
	tl.start = 0
	tl.clamp()
}

// zoom zooms by f around x, in DIPs from the left edge.
func (tl *timeline) zoom(f float64, x float32) {
	at := tl.t(x)
	tl.pxPerSec *= f
	tl.clamp()
	tl.start = at - float64(x)/tl.pxPerSec
	tl.clamp()
}

// pan moves the view by d seconds.
func (tl *timeline) pan(d float64) {
	tl.start += d
	tl.clamp()
}

// reveal scrolls lo to hi into view, zooming out if it is wider than the view.
func (tl *timeline) reveal(lo, hi float64) {
	if tl.width <= 0 {
		return
	}
	span := tl.span()
	if hi-lo > span*0.95 {
		tl.pxPerSec = float64(tl.width) / ((hi - lo) * 1.04)
		tl.start = lo - (hi-lo)*0.02
	} else if lo < tl.start || hi > tl.start+span {
		tl.start = (lo+hi)/2 - span/2
	}
	tl.clamp()
}

// follow keeps the playhead in view as it plays.
func (tl *timeline) follow(t float64) {
	span := tl.span()
	if tl.drag == dragNone && (t > tl.start+span*0.92 || t < tl.start) {
		tl.start = t - span*0.08
		tl.clamp()
	}
}

// lanes returns the boxes of the audio lane, the video lane and the scroll
// bar in the timeline's box r.
func (a *app) lanes(r ui.Rect) (audio, video, bar ui.Rect) {
	h := max((r.H-rulerH-scrollH-3*laneGap)/2, minLaneH)
	audio = ui.Rect{X: r.X, Y: r.Y + rulerH + laneGap, W: r.W, H: h}
	video = ui.Rect{X: r.X, Y: audio.Y + h + laneGap, W: r.W, H: h}
	bar = ui.Rect{X: r.X, Y: r.Y + r.H - scrollH, W: r.W, H: scrollH}
	return
}

// placedClips returns the clips on the timeline, the selected one last, as
// they are drawn, the topmost last.
func (a *app) placedClips() []*clip {
	var out []*clip
	var sel *clip
	for _, c := range a.clips {
		switch {
		case !c.placed():
		case c.id == a.selected:
			sel = c
		default:
			out = append(out, c)
		}
	}
	if sel != nil {
		out = append(out, sel)
	}
	return out
}

// hitClip returns the topmost clip under (x, y), relative to the timeline.
func (a *app) hitClip(x, y float32) *clip {
	r := ui.Rect{W: a.tl.width, H: a.tl.height}
	_, lane, _ := a.lanes(r)
	if y < lane.Y || y >= lane.Y+lane.H {
		return nil
	}
	cs := a.placedClips()
	for i := len(cs) - 1; i >= 0; i-- {
		c := cs[i]
		if x >= a.tl.x(c.offset, r)-2 && x <= a.tl.x(c.end(), r)+2 {
			return c
		}
	}
	return nil
}

// conflict is two clips on the video lane that overlap.
type conflict struct {
	a, b     *clip
	from, to float64
}

// conflicts returns the clips that overlap one another, by more than a
// millisecond, in time order.
func (a *app) conflicts() []conflict {
	var cs []*clip
	for _, c := range a.clips {
		if c.placed() {
			cs = append(cs, c)
		}
	}
	sort.Slice(cs, func(i, j int) bool { return cs[i].offset < cs[j].offset })
	var out []conflict
	for i, x := range cs {
		for _, y := range cs[i+1:] {
			if y.offset >= x.end()-0.001 {
				break
			}
			out = append(out, conflict{x, y, y.offset, math.Min(x.end(), y.end())})
		}
	}
	return out
}

// conflictsOf returns the conflicts c is in.
func (a *app) conflictsOf(c *clip) []conflict {
	var out []conflict
	for _, k := range a.conflicts() {
		if k.a == c || k.b == c {
			out = append(out, k)
		}
	}
	return out
}

// describeConflicts writes the conflicts for the user, at most six.
func describeConflicts(cs []conflict) string {
	var b strings.Builder
	for i, k := range cs {
		if i == 6 {
			fmt.Fprintf(&b, "……另有 %d 处\n", len(cs)-6)
			break
		}
		fmt.Fprintf(&b, "· %s 与 %s 在 %s 处重叠 %.1f 秒\n", k.a.name(), k.b.name(), fmtTime(k.from), k.to-k.from)
	}
	return b.String()
}

// timelineInput takes the pointer and the wheel over the timeline.
func (a *app) timelineInput(ev ui.InputEvent) bool {
	tl := &a.tl
	r := ui.Rect{W: tl.width, H: tl.height}
	_, _, bar := a.lanes(r)
	switch ev.Kind {
	case ui.InputPointerDown:
		tl.moved = false
		tl.dragX = ev.X
		switch {
		case ev.Button == 2 || ev.Button == 0 && ev.Mods&ui.Alt != 0:
			tl.drag, tl.dragStart = dragPan, tl.start
		case ev.Button != 0:
			return false
		case ev.Y >= bar.Y:
			// The scroll bar: grab the thumb, or page to where it was hit.
			x0, x1 := tl.thumb(bar)
			if ev.X < x0 || ev.X > x1 {
				tl.pan(float64(ev.X-(x0+x1)/2) / float64(bar.W) * tl.total())
			}
			tl.drag, tl.dragStart = dragScroll, tl.start
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
			if c := a.hitClip(ev.X, ev.Y); c != nil {
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
			tl.clamp()
		case dragScroll:
			tl.start = tl.dragStart + float64(ev.X-tl.dragX)/float64(bar.W)*tl.total()
			tl.clamp()
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
			a.warnConflicts(a.clipByID(tl.dragClip))
		}
		tl.drag = dragNone
		return true
	case ui.InputScroll:
		a.timelineWheel(ev)
		return true
	case ui.InputZoom:
		tl.zoom(float64(ev.Scale), ev.X)
		return true
	}
	return false
}

// notch is how far Windows scrolls for a notch of a mouse wheel, in DIPs,
// at the default three lines.
const notch = 99

// timelineWheel zooms with the wheel, with or without Ctrl or Alt, around the
// pointer, and scrolls sideways with Shift or a sideways scroll.
func (a *app) timelineWheel(ev ui.InputEvent) {
	tl := &a.tl
	dx, dy := float64(ev.DX), float64(ev.DY)
	mods := ev.Mods | a.held // as held while the view last built, too
	sideways := mods&ui.Shift != 0 || math.Abs(dx) > math.Abs(dy)
	if !sideways {
		// A notch zooms by 1.3 either way; a touchpad as far as it moves.
		tl.zoom(math.Pow(1.3, -dy/notch), ev.X)
		return
	}
	d := dx
	if d == 0 {
		d = dy
	}
	if ev.Precise {
		tl.pan(d / tl.pxPerSec)
	} else {
		// A notch pans a tenth of the view.
		tl.pan(d / notch * tl.span() / 10)
	}
}

// total is the length the scroll bar stands for.
func (tl *timeline) total() float64 { return math.Max(tl.end, tl.start+tl.span()) }

// thumb returns the scroll bar's thumb, from x0 to x1.
func (tl *timeline) thumb(bar ui.Rect) (x0, x1 float32) {
	total := tl.total()
	if total <= 0 {
		return bar.X, bar.X + bar.W
	}
	x0 = bar.X + float32(tl.start/total)*bar.W
	x1 = bar.X + float32(math.Min((tl.start+tl.span())/total, 1))*bar.W
	if x1-x0 < 24 {
		x1 = min(x0+24, bar.X+bar.W)
		x0 = x1 - 24
	}
	return
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

// warnConflicts tells, in the status bar, what c now overlaps.
func (a *app) warnConflicts(c *clip) {
	if c == nil {
		return
	}
	if cs := a.conflictsOf(c); len(cs) > 0 {
		a.setStatus("时间轴冲突："+strings.TrimSpace(strings.ReplaceAll(describeConflicts(cs), "\n", "  ")), false)
	}
}

// drawTimeline paints the ruler, the two lanes, the scroll bar and the
// playhead.
func (a *app) drawTimeline(p *ui.Painter, r ui.Rect) {
	t := a.theme
	tl := &a.tl
	p.Fill(r, ui.Hex("#141518"), 0)
	_, hi := a.timeRange()
	sel := a.selectedClip()
	laneA, laneV, bar := a.lanes(r)

	p.Clip(ui.Rect{X: r.X, Y: r.Y + rulerH, W: r.W, H: bar.Y - r.Y - rulerH}, 0, func() {
		if x := tl.x(hi, r); x < r.X+r.W {
			p.Fill(ui.Rect{X: x, Y: r.Y, W: r.X + r.W - x, H: r.H}, ui.Hex("#0c0d0f"), 0)
		}

		// Match source 1: the external audio, the files one after another.
		p.Fill(laneA, ui.Hex("#1b1d22"), 0)
		p.Clip(laneA, 0, func() {
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
					drawWave(p, tl, r, it.peaks, it.peaks.Scale(), it.start, 1, box, ui.Hex("#3ecf8e"))
				} else if it.loading {
					p.Text(x0+8, box.Y+box.H/2-7, fmt.Sprintf("解码中 %d%%", int(it.progress*100)), 11, t.TextMuted)
				}
				p.Line(x0, box.Y, x0, box.Y+box.H, 1, ui.Hex("#3ecf8e").Alpha(0.6))
				p.Clip(box, 4, func() {
					p.Text(x0+6, box.Y+3, fmt.Sprintf("%d. %s", i+1, it.name()), 11, ui.Hex("#a7f3d0"))
				})
			}
			p.Text(r.X+8, laneA.Y+laneA.H-16, "匹配源 1 · 外部音频（拼接）", 10, t.TextMuted)
		})

		// Match source 2: the videos matched or placed, on one lane.
		p.Fill(laneV, ui.Hex("#18191d"), 0)
		p.Clip(laneV, 0, func() {
			for _, c := range a.placedClips() {
				x0, x1 := tl.x(c.offset, r), tl.x(c.end(), r)
				box := ui.Rect{X: x0, Y: laneV.Y + 2, W: max(x1-x0, 2), H: laneV.H - 4}
				isSel := sel != nil && c.id == sel.id
				bg := ui.Hex("#232838")
				if isSel {
					bg = ui.Hex("#283252")
				} else if tl.hoverClip == c.id {
					bg = ui.Hex("#272c3d")
				}
				p.Fill(box, bg, 5)
				if c.peaks != nil {
					drawWave(p, tl, r, c.peaks, c.peaks.Scale(), c.soundAt(), 1+c.drift, box, ui.Hex("#7aa2ff"))
				}
				border := ui.Hex("#3a4260")
				if isSel {
					border = t.Accent
				}
				p.Stroke(box, border, 5, 1.5)
				p.Clip(box, 5, func() {
					label := c.name()
					if c.aligned {
						label += fmt.Sprintf("   %s · 置信度 %d%%", fmtTime(c.offset), int(c.confidence*100+0.5))
					} else {
						label += "   " + fmtTime(c.offset) + " · 手动放置"
					}
					lx := max(x0, r.X) + 8
					p.Fill(ui.Rect{X: lx - 4, Y: box.Y + 3, W: float32(len([]rune(label)))*7 + 8, H: 16}, bg.Alpha(0.8), 3)
					p.Text(lx, box.Y+4, label, 11, ui.Hex("#dbe4ff"))
				})
			}
			// Where clips overlap, in red: the user decides which is right.
			red := ui.Hex("#ff5d5d")
			for _, k := range a.conflicts() {
				x0, x1 := tl.x(k.from, r), tl.x(k.to, r)
				box := ui.Rect{X: x0, Y: laneV.Y + 2, W: max(x1-x0, 2), H: laneV.H - 4}
				p.Fill(box, red.Alpha(0.3), 0)
				p.Stroke(box, red, 0, 1.5)
				if x1-x0 > 40 {
					p.Text(x0+4, box.Y+box.H-17, "冲突", 11, red)
				}
			}
			if len(a.placedClips()) == 0 {
				msg := "匹配源 2 · 视频：匹配完成后放到这里"
				if len(a.clips) == 0 {
					msg = "匹配源 2 · 视频：添加视频并点击「自动对齐」"
				}
				p.Text(r.X+8, laneV.Y+laneV.H/2-7, msg, 11, t.TextMuted)
			} else {
				p.Text(r.X+8, laneV.Y+laneV.H-16, "匹配源 2 · 视频", 10, t.TextMuted)
			}
		})
	})

	// The ruler, from zero.
	ruler := ui.Rect{X: r.X, Y: r.Y, W: r.W, H: rulerH}
	p.Fill(ruler, ui.Hex("#202227"), 0)
	p.Line(r.X, r.Y+rulerH-1, r.X+r.W, r.Y+rulerH-1, 1, t.Border)
	major, minor := tickSteps(tl.pxPerSec)
	first := math.Max(0, math.Floor(tl.start/minor)*minor)
	end := tl.t(r.W)
	for i := 0; ; i++ {
		v := first + float64(i)*minor
		if v > end {
			break
		}
		x := tl.x(v, r)
		if isMajor := math.Abs(math.Remainder(v, major)) < minor*0.01; isMajor {
			p.Line(x, r.Y+rulerH-12, x, r.Y+rulerH, 1, t.TextMuted)
			p.Text(x+4, r.Y+4, fmtTick(v, major), 10, t.TextMuted)
		} else {
			p.Line(x, r.Y+rulerH-5, x, r.Y+rulerH, 1, t.Border)
		}
	}

	// The scroll bar.
	p.Fill(bar, ui.Hex("#1a1b1f"), 0)
	x0, x1 := tl.thumb(bar)
	thumb := ui.Hex("#4a4e58")
	if tl.drag == dragScroll {
		thumb = ui.Hex("#6a6f7c")
	}
	p.Fill(ui.Rect{X: x0, Y: bar.Y + 3, W: x1 - x0, H: bar.H - 6}, thumb, 4)

	// The playhead.
	if x := tl.x(a.playhead, r); x >= r.X && x <= r.X+r.W {
		red := ui.Hex("#ff5d5d")
		p.Line(x, r.Y, x, bar.Y, 1.5, red)
		var head ui.Path
		head.MoveTo(x-6, r.Y).LineTo(x+6, r.Y).LineTo(x, r.Y+9).Close()
		p.FillPath(&head, red)
	}
}

// drawWave paints peaks whose first frame plays at timeline time at, and
// whose second lasts stretch seconds of the timeline, inside box, as a filled
// envelope.
func drawWave(p *ui.Painter, tl *timeline, r ui.Rect, pk *wave.Peaks, gain float32, at, stretch float64, box ui.Rect, col ui.Color) {
	x0 := max(box.X, r.X, tl.x(at, r))
	x1 := min(box.X+box.W, r.X+r.W, tl.x(at+float64(pk.Frames)/media.PlayRate*stretch, r))
	if x1 <= x0 {
		return
	}
	mid := box.Y + box.H/2
	amp := (box.H/2 - 3) * gain
	fpp := media.PlayRate / tl.pxPerSec / stretch // frames a DIP
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
		f := (tl.t(x-r.X) - at) * media.PlayRate / stretch
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
