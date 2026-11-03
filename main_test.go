package main

import (
	"image/png"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"wavealign/internal/media"
)

func ffmpegRun(t *testing.T, args ...string) {
	t.Helper()
	mp, _ := media.Tools()
	out, err := exec.Command(mp, append([]string{"-v", "error", "-y"}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("ffmpeg %v: %v\n%s", args, err, out)
	}
}

// drain runs what goroutines posted until done holds.
func drain(t *testing.T, a *app, tt *ui.Tester, what string, done func() bool) {
	t.Helper()
	deadline := time.After(60 * time.Second)
	for !done() {
		select {
		case fn := <-a.queue:
			fn()
		case <-deadline:
			t.Fatalf("timed out waiting for %s", what)
		case <-time.After(20 * time.Millisecond):
		}
		if tt != nil {
			tt.Frame()
		}
	}
	// What is left, as frames arriving.
	for {
		select {
		case fn := <-a.queue:
			fn()
		default:
			return
		}
	}
}

func snapshot(t *testing.T, tt *ui.Tester, name string) {
	t.Helper()
	dir := os.Getenv("WAVEALIGN_SNAPSHOTS")
	if dir == "" {
		return
	}
	os.MkdirAll(dir, 0o755)
	f, err := os.Create(filepath.Join(dir, name+".png"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	png.Encode(f, tt.Image())
}

func TestApp(t *testing.T) {
	if err := media.Configure(""); err != nil {
		t.Skip(err)
	}
	dir := t.TempDir()
	t.Setenv("LOCALAPPDATA", dir)
	t.Setenv("APPDATA", dir)

	master := filepath.Join(dir, "master.wav")
	ffmpegRun(t, "-f", "lavfi", "-i", "anoisesrc=d=70:c=pink:r=48000:a=0.5:seed=3",
		"-af", "volume='if(gt(sin(2*PI*t*1.3)+sin(2*PI*t*0.41),0.2),1,0.04)':eval=frame", "-ac", "2", master)
	a1 := filepath.Join(dir, "rec_part1.wav")
	a2 := filepath.Join(dir, "rec_part2.mp3")
	ffmpegRun(t, "-i", master, "-t", "30", a1)
	ffmpegRun(t, "-ss", "30", "-i", master, "-b:a", "192k", a2)
	cam1 := filepath.Join(dir, "camA.mp4")
	ffmpegRun(t, "-f", "lavfi", "-i", "testsrc2=d=20:s=640x360:r=30",
		"-ss", "12.5", "-t", "20", "-i", master,
		"-f", "lavfi", "-i", "sine=f=330:d=20",
		"-map", "0:v", "-map", "1:a", "-map", "2:a", "-af:a:0", "highpass=f=200,volume=0.25",
		"-c:v", "libx264", "-preset", "ultrafast", "-c:a", "aac", "-shortest", cam1)
	cam2 := filepath.Join(dir, "camB.mov")
	ffmpegRun(t, "-f", "lavfi", "-i", "testsrc=d=16:s=640x360:r=25",
		"-ss", "41.2", "-t", "16", "-i", master,
		"-map", "0:v", "-map", "1:a", "-af", "lowpass=f=2500,volume=0.5",
		"-c:v", "libx264", "-preset", "ultrafast", "-c:a", "pcm_s16le", "-ac", "1", "-shortest", cam2)
	// A third camera that filmed while camA did: the two overlap.
	cam3 := filepath.Join(dir, "camC.mp4")
	ffmpegRun(t, "-f", "lavfi", "-i", "testsrc2=d=10:s=640x360:r=30",
		"-ss", "20", "-t", "10", "-i", master,
		"-map", "0:v", "-map", "1:a", "-c:v", "libx264", "-preset", "ultrafast", "-c:a", "aac", "-shortest", cam3)

	a := newApp()
	a.queue = make(chan func(), 4096)
	a.configureFFmpeg()
	t.Cleanup(a.close)
	tt := ui.NewTester(a.view, 1440, 920)
	snapshot(t, tt, "0-empty")

	a.addAudios([]string{a1, a2}, nil)
	a.addClips([]string{cam1, cam2, cam3}, nil)
	ready := func() bool {
		for _, it := range a.audios {
			if it.loading || it.pcm == nil {
				return false
			}
		}
		for _, c := range a.clips {
			if c.loading || c.pcm == nil {
				return false
			}
		}
		return true
	}
	drain(t, a, tt, "decoding", ready)
	if got := a.extEnd(); math.Abs(got-70) > 0.1 {
		t.Fatalf("external audio lasts %.3f s", got)
	}
	if len(a.clips[0].streams) != 2 {
		t.Fatalf("camA has %d audio streams", len(a.clips[0].streams))
	}
	// Not matched yet: no clip is on the timeline, which starts at zero.
	if lo, hi := a.timeRange(); lo != 0 || math.Abs(hi-70) > 0.1 || len(a.placedClips()) != 0 {
		t.Fatalf("timeline %.2f to %.2f with %d clips before matching", lo, hi, len(a.placedClips()))
	}

	if err := tt.Click("自动对齐全部"); err != nil {
		t.Fatal(err)
	}
	drain(t, a, tt, "matching", func() bool {
		for _, c := range a.clips {
			if c.aligning || !c.aligned && c.err == "" {
				return false
			}
		}
		return true
	})
	for i, want := range []float64{12.5, 41.2, 20} {
		c := a.clips[i]
		if c.err != "" {
			t.Fatalf("%s: %s", c.name(), c.err)
		}
		if math.Abs(c.offset-want) > 0.002 {
			t.Fatalf("%s at %.4f, want %.4f (confidence %.2f)", c.name(), c.offset, want, c.confidence)
		}
		t.Logf("%s at %.5f, confidence %.2f, drift %.1f ppm", c.name(), c.offset, c.confidence, c.driftPPM)
	}
	// camC overlaps camA on the one video lane: the user is told, rather
	// than given a lane of its own.
	if !a.conflictOpen || len(a.conflictList) != 1 || len(a.conflicts()) != 1 {
		t.Fatalf("conflict dialog %v with %d conflicts", a.conflictOpen, len(a.conflictList))
	}
	a.tl.fitPending = true
	a.seek(20)
	drain(t, a, tt, "preview", func() bool { return a.prev.has })
	tt.Frame()
	snapshot(t, tt, "1-conflict")
	if err := tt.Click("知道了"); err != nil {
		t.Fatal(err)
	}
	if a.conflictOpen {
		t.Fatal("the conflict dialog stayed open")
	}
	tt.Frame()
	snapshot(t, tt, "1-aligned")
	a.saved = string(a.snapshot().json())
	if a.dirty() {
		t.Fatal("dirty right after saving")
	}

	// Nudge camA with the keys.
	before := a.clips[0].offset
	a.selectClip(a.clips[0].id)
	tt.Key(0, ui.KeyRight)
	if d := a.clips[0].offset - before; math.Abs(d-1.0/30) > 1e-9 {
		t.Fatalf("→ moved the clip by %.5f s, want one frame", d)
	}
	tt.Key(ui.Alt, ui.KeyLeft)
	if d := a.clips[0].offset - before; math.Abs(d-(1.0/30-0.001)) > 1e-9 {
		t.Fatalf("Alt+← left the clip %.5f s off", d)
	}
	if !a.dirty() {
		t.Fatal("nudging a clip left the project unchanged")
	}

	// The wheel zooms around the pointer, with or without Ctrl or Alt; the
	// view never shows time below zero.
	a.tl.fit()
	tt.Frame()
	box := a.tl.box
	_, vlane, _ := a.lanes(box)
	px := box.X + box.W/2
	at := a.tl.t(px - box.X)
	z := a.tl.pxPerSec
	tt.Wheel(px, vlane.Y+10, 0, -notch)
	if r := a.tl.pxPerSec / z; math.Abs(r-1.3) > 1e-6 {
		t.Fatalf("a notch zoomed by %.4f", r)
	}
	if d := a.tl.t(px-box.X) - at; math.Abs(d) > 1e-6 {
		t.Fatalf("the time under the pointer moved by %.6f s", d)
	}
	for _, mods := range []ui.Modifiers{ui.Ctrl, ui.Alt} {
		tt.HoldModifiers(mods)
		tt.Frame() // the tester's wheel carries no modifiers: the view reads them
		z = a.tl.pxPerSec
		tt.Wheel(px, vlane.Y+10, 0, -notch)
		if r := a.tl.pxPerSec / z; math.Abs(r-1.3) > 1e-6 {
			t.Fatalf("a notch with %v zoomed by %.4f", mods, r)
		}
	}
	tt.HoldModifiers(ui.Shift)
	tt.Frame()
	start := a.tl.start
	tt.Wheel(px, vlane.Y+10, 0, notch)
	if a.tl.start <= start || math.Abs(a.tl.pxPerSec/z/1.3-1) > 1e-6 {
		t.Fatalf("Shift+wheel moved the view from %.3f to %.3f", start, a.tl.start)
	}
	for range 50 {
		// To the left, past the start. The tester's events reset the keys
		// held, which the real backend sends with each.
		tt.HoldModifiers(ui.Shift)
		tt.Frame()
		tt.Wheel(px, vlane.Y+10, 0, -notch)
	}
	tt.HoldModifiers(0)
	tt.Frame()
	if a.tl.start != 0 {
		t.Fatalf("panned to %.3f, before zero", a.tl.start)
	}
	for range 40 {
		tt.Wheel(box.X+5, vlane.Y+10, 0, notch) // zoom out, far
	}
	if a.tl.start < 0 || a.tl.pxPerSec < a.tl.minPxPerSec()-1e-9 {
		t.Fatalf("zoomed out to %.4f DIPs/s from %.3f", a.tl.pxPerSec, a.tl.start)
	}

	// Drag camB's clip 60 DIPs to the right on the timeline.
	a.tl.fit()
	tt.Frame()
	camB := a.clips[1]
	from := camB.offset
	x := a.tl.x(camB.offset+2, a.tl.box)
	y := vlane.Y + vlane.H/2
	tt.Press(x, y)
	tt.Move(x+30, y)
	tt.Move(x+60, y)
	tt.Release(x+60, y)
	if a.selected != camB.id {
		t.Fatal("pressing camB did not select it")
	}
	if got, want := camB.offset-from, 60/a.tl.pxPerSec; math.Abs(got-want) > 1e-6 {
		t.Fatalf("dragged by %.4f s, want %.4f", got, want)
	}
	camB.offset = from

	// Space plays and pauses; a click on the ruler seeks.
	tt.Key(0, ui.KeySpace)
	if !a.playing || a.player == nil {
		t.Fatal("space did not play")
	}
	tt.ClickAt(a.tl.x(30, a.tl.box), a.tl.box.Y+10)
	if math.Abs(a.playhead-30) > 0.2 {
		t.Fatalf("playhead at %.3f after clicking the ruler at 30 s", a.playhead)
	}
	// While it plays, the preview shows camA's frames as they come due,
	// decoded on the GPU.
	a.cfg.HWAccel = "cuda"
	a.seek(14)
	tt.Frame()
	began := time.Now()
	drain(t, a, tt, "playing", func() bool { return time.Since(began) > 1500*time.Millisecond })
	t.Logf("playhead %.3f after 1.5 s from 14, %d frames shown, decoder %s", a.playhead, a.prev.shown, a.prev.mode)
	if a.prev.shown < 20 {
		t.Fatalf("only %d frames shown in 1.5 s of playback", a.prev.shown)
	}
	tt.Key(0, ui.KeySpace)
	if a.playing {
		t.Fatal("space did not pause")
	}

	// Zoom in on camA's start and look.
	a.tl.reveal(a.clips[0].offset-0.5, a.clips[0].offset+1.5)
	tt.Frame()
	snapshot(t, tt, "2-zoomed")

	// Back to the matched place, and export both.
	a.selectClip(a.clips[0].id)
	tt.Frame()
	if err := tt.Click("复位"); err != nil {
		t.Fatal(err)
	}
	if a.clips[0].offset != a.clips[0].autoOffset {
		t.Fatal("复位 did not restore the matched offset")
	}
	a.cfg.OutDir = filepath.Join(dir, "out")
	os.MkdirAll(a.cfg.OutDir, 0o755)
	a.exportOpen = true
	tt.Frame()
	snapshot(t, tt, "3-export")
	if err := tt.Click("开始导出"); err != nil {
		t.Fatal(err)
	}
	drain(t, a, tt, "export", func() bool { return !a.exporting })
	for _, c := range a.clips {
		if c.exportErr != "" || c.exported == "" {
			t.Fatalf("%s: exported %q, error %q", c.name(), c.exported, c.exportErr)
		}
		info, err := media.Probe(t.Context(), c.exported)
		if err != nil {
			t.Fatal(err)
		}
		v, _ := info.Video()
		src, _ := c.info.Video()
		if v.Codec != src.Codec || math.Abs(info.Duration-c.info.Duration) > 0.1 {
			t.Fatalf("%s: %s %.3f s, source %s %.3f s", filepath.Base(c.exported), v.Codec, info.Duration, src.Codec, c.info.Duration)
		}
		t.Logf("exported %s: %d audio streams", filepath.Base(c.exported), len(info.Audio()))
	}
	tt.Frame()
	snapshot(t, tt, "4-exported")
}
