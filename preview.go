package main

import (
	"context"
	"image"
	"time"

	"github.com/egoist/mygo/ui"

	"wavealign/internal/media"
)

// The preview's frames: decoded by ffmpeg, on the GPU where it can, scaled to
// this size, letterboxed.
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
	has   bool   // a frame of the clip shown is in bmp
	label string // why there is none
	// mode is the decoder that worked, once one has; "" until then.
	mode  string
	shown int // frames shown while playing
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
		if !c.placed() || c.info == nil || a.playhead < c.offset || a.playhead >= c.end() {
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

// decoders returns the decoders to try: the one that worked, else the chain
// the settings ask for.
func (a *app) decoders() []string {
	if a.prev.mode != "" {
		return []string{a.prev.mode}
	}
	return media.DecodeChain(a.cfg.HWAccel)
}

// frameOf describes the preview frames of a clip.
func frameOf(c *clip) media.Frame {
	f := media.Frame{W: prevW, H: prevH}
	if v, ok := c.info.Video(); ok {
		f.SrcW, f.SrcH, f.Rotation = v.Width, v.Height, v.Rotation
	}
	return f
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
		a.streamFrames(ctx, c, local)
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
	id, path, f, modes := c.id, c.path, frameOf(c), a.decoders()
	go func() {
		// Wait for the scrubbing to rest a moment.
		select {
		case <-ctx.Done():
			return
		case <-time.After(40 * time.Millisecond):
		}
		var img *image.RGBA
		var err error
		mode := ""
		for _, mode = range modes {
			img, err = media.GrabFrame(ctx, path, float64(key)/prevFPS, f, mode)
			if err == nil || ctx.Err() != nil {
				break
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
				p.mode = mode
				p.bmp.Update(img)
				p.has = true
			}
		})
	}()
}

// streamFrames decodes the clip from local on as it plays, and shows each
// frame as the playhead reaches it. A frame that comes late still shows if
// none did for a while, so the picture keeps moving while the decoder
// catches up; one that falls far behind starts decoding again from where the
// playhead is.
func (a *app) streamFrames(ctx context.Context, c *clip, local float64) {
	p := &a.prev
	id, path, offset, f, modes, gen := c.id, c.path, c.offset, frameOf(c), a.decoders(), p.gen
	now := func() (float64, bool) {
		cl := a.clock.Load()
		if cl == nil {
			return 0, false
		}
		return cl.origin + cl.player.Position().Seconds() - offset, true
	}
	go func() {
		k := 0
		var lastShown time.Time
		each := func(img *image.RGBA) bool {
			at := local + float64(k)/prevFPS
			k++
			for {
				t, ok := now()
				if !ok || ctx.Err() != nil {
					return false
				}
				if late := t - at; late > -0.5/prevFPS {
					if late > 1.5 {
						// Far behind: start again where the playhead is.
						a.post(func() {
							if p.cur.gen == gen {
								p.seeked()
							}
						})
						return false
					}
					if late > 0.1 && time.Since(lastShown) < 120*time.Millisecond {
						return true // late, and one showed just now: skip it
					}
					break
				}
				time.Sleep(min(time.Duration((at-t)*float64(time.Second)), 10*time.Millisecond))
			}
			lastShown = time.Now()
			a.post(func() {
				if ctx.Err() == nil && p.cur.clip == id {
					p.bmp.Update(img)
					p.has = true
					p.shown++
				}
			})
			return true
		}
		var err error
		for _, mode := range modes {
			var n int
			n, err = media.StreamFrames(ctx, path, local, f, prevFPS, mode, each)
			if n > 0 || err == nil {
				if n > 0 {
					a.post(func() { p.mode = mode })
				}
				break
			}
			if ctx.Err() != nil {
				return
			}
		}
		if err != nil && ctx.Err() == nil {
			a.post(func() { p.has, p.label = false, "无法解码画面: "+err.Error() })
		}
	}()
}
