package main

import (
	"context"
	"image"
	"time"

	"github.com/egoist/mygo/ui"

	"wavealign/internal/media"
)

// The preview's frames: decoded by ffmpeg (on the GPU where it can), scaled
// to this size, letterboxed.
const (
	prevW   = 640
	prevH   = 360
	prevFPS = 30.0
)

// preview shows the frame of the video under the playhead.
type preview struct {
	bmp    *ui.Bitmap
	gen    int // bumped by seeks and clip moves
	cancel context.CancelFunc
	cur    struct {
		clip      int
		gen       int
		streaming bool
		key       int64
		path      string
	}
	has      bool   // a frame of the clip shown is in bmp
	label    string // why there is none
	hwFailed bool   // hardware decoding failed: decode in software
	shown    int    // frames shown while playing, for tests
}

func (p *preview) init() {
	black := image.NewRGBA(image.Rect(0, 0, prevW, prevH))
	for i := range black.Pix {
		if i%4 == 3 {
			black.Pix[i] = 255
		}
	}
	p.bmp = ui.NewBitmap(black)
}

// seeked restarts what plays, after the playhead jumped or a clip moved.
func (p *preview) seeked() { p.gen++ }

func (p *preview) stop() {
	if p.cancel != nil {
		p.cancel()
		p.cancel = nil
	}
	p.cur.streaming = false
}

// previewClip returns the clip the preview shows: the selected one where the
// playhead is in it, else the first under the playhead.
func (a *app) previewClip() *clip {
	in := func(c *clip) bool {
		if c.info == nil || a.playhead < c.offset || a.playhead >= c.end() {
			return false
		}
		_, ok := c.info.Video()
		return ok
	}
	if c := a.selectedClip(); c != nil && in(c) {
		return c
	}
	for _, c := range a.clips {
		if in(c) {
			return c
		}
	}
	return nil
}

func (a *app) hwaccel() string {
	if a.prev.hwFailed {
		return "none"
	}
	return a.cfg.HWAccel
}

// updatePreview starts decoding what the preview should show, from the view.
func (a *app) updatePreview() {
	p := &a.prev
	c := a.previewClip()
	if c == nil {
		p.stop()
		p.cur.clip = 0
		p.has = false
		p.label = "播放头处没有视频"
		return
	}
	local := a.playhead - c.offset
	if p.cur.clip != c.id {
		p.has = false
		p.label = "加载画面…"
	}
	if a.playing {
		if p.cur.streaming && p.cur.clip == c.id && p.cur.gen == p.gen {
			return
		}
		p.stop()
		ctx, cancel := context.WithCancel(context.Background())
		p.cancel = cancel
		p.cur.clip, p.cur.gen, p.cur.streaming, p.cur.path = c.id, p.gen, true, c.path
		a.streamFrames(ctx, c.id, c.path, local, c.offset)
		return
	}
	key := int64(local * prevFPS)
	if !p.cur.streaming && p.cur.clip == c.id && p.cur.key == key && p.cur.path == c.path {
		return
	}
	p.stop()
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel
	p.cur.clip, p.cur.key, p.cur.path = c.id, key, c.path
	id, path, hw := c.id, c.path, a.hwaccel()
	go func() {
		// Wait for the scrubbing to rest a moment.
		select {
		case <-ctx.Done():
			return
		case <-time.After(40 * time.Millisecond):
		}
		img, err := media.GrabFrame(ctx, path, float64(key)/prevFPS, prevW, prevH, hw)
		if err != nil && hw != "none" && ctx.Err() == nil {
			img, err = media.GrabFrame(ctx, path, float64(key)/prevFPS, prevW, prevH, "none")
			if err == nil {
				a.post(func() { p.hwFailed = true })
			}
		}
		a.post(func() {
			if ctx.Err() != nil || p.cur.clip != id {
				return
			}
			switch {
			case err != nil:
				p.has, p.label = false, "无法解码画面: "+err.Error()
			case img == nil:
				p.has, p.label = false, "此处没有画面"
			default:
				p.bmp.Update(img)
				p.has = true
			}
		})
	}()
}

// streamFrames decodes the clip from local on as it plays, and shows each
// frame as the playhead reaches it.
func (a *app) streamFrames(ctx context.Context, id int, path string, local, offset float64) {
	p := &a.prev
	hw := a.hwaccel()
	now := func() (float64, bool) {
		cl := a.clock.Load()
		if cl == nil {
			return 0, false
		}
		return cl.origin + cl.player.Position().Seconds() - offset, true
	}
	go func() {
		k := 0
		each := func(img *image.RGBA) bool {
			at := local + float64(k)/prevFPS
			k++
			for {
				t, ok := now()
				if !ok || ctx.Err() != nil {
					return false
				}
				if t >= at-0.5/prevFPS {
					if t-at > 0.25 {
						return true // late: drop it
					}
					break
				}
				time.Sleep(min(time.Duration((at-t)*float64(time.Second)), 10*time.Millisecond))
			}
			a.post(func() {
				if ctx.Err() == nil && p.cur.clip == id {
					p.bmp.Update(img)
					p.has = true
					p.shown++
				}
			})
			return true
		}
		err := media.StreamFrames(ctx, path, local, prevW, prevH, prevFPS, hw, each)
		if err != nil && k == 0 && hw != "none" && ctx.Err() == nil {
			a.post(func() { p.hwFailed = true })
			err = media.StreamFrames(ctx, path, local, prevW, prevH, prevFPS, "none", each)
		}
		if err != nil && ctx.Err() == nil {
			a.post(func() { p.has, p.label = false, "无法解码画面: "+err.Error() })
		}
	}()
}
