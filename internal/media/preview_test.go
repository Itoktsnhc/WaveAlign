package media

import (
	"context"
	"image"
	"path/filepath"
	"testing"
	"time"
)

// TestPreview4K streams a 4K HEVC 10-bit 60 fps clip, as cameras record, on
// the GPU, and checks it decodes well ahead of real time.
func TestPreview4K(t *testing.T) {
	if err := Configure(""); err != nil {
		t.Skip(err)
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "hevc4k.mp4")
	mp, _ := Tools()
	if out, err := command(context.Background(), false, "-v", "error", "-y", "-f", "lavfi",
		"-i", "testsrc2=d=6:s=3840x2160:r=60000/1001", "-c:v", "hevc_nvenc", "-pix_fmt", "p010le",
		"-profile:v", "main10", "-b:v", "80M", src); err != nil || out.Run() != nil {
		t.Skipf("no NVENC to make a 4K HEVC clip with %s", mp)
	}
	info, err := Probe(context.Background(), src)
	if err != nil {
		t.Fatal(err)
	}
	v, _ := info.Video()
	f := Frame{W: 640, H: 360, SrcW: v.Width, SrcH: v.Height}
	for _, mode := range []string{DecodeCUDA, DecodeCPU} {
		start := time.Now()
		n, err := StreamFrames(context.Background(), src, 1, f, 30, mode, func(*image.RGBA) bool { return true })
		if err != nil {
			t.Fatalf("%s: %v", mode, err)
		}
		el := time.Since(start)
		speed := float64(n) / 30 / el.Seconds()
		t.Logf("%s: %d frames of 4K HEVC in %v, %.1f× real time", DecodeLabel(mode), n, el.Round(time.Millisecond), speed)
		if mode == DecodeCUDA && speed < 2 {
			t.Errorf("the GPU decodes only %.1f× real time", speed)
		}
	}
}

// TestPreviewRotation checks a phone's portrait clip shows upright, on the GPU
// as on the CPU.
func TestPreviewRotation(t *testing.T) {
	if err := Configure(""); err != nil {
		t.Skip(err)
	}
	dir := t.TempDir()
	plain := filepath.Join(dir, "plain.mp4")
	src := filepath.Join(dir, "portrait.mp4")
	// A landscape picture, white on its left half, then tagged to turn 90°,
	// as phones record portrait video.
	for _, args := range [][]string{
		{"-f", "lavfi", "-i", "color=white:s=320x180:d=1,drawbox=x=160:y=0:w=160:h=180:color=black:t=fill",
			"-c:v", "libx264", "-pix_fmt", "yuv420p", plain},
		{"-display_rotation", "-90", "-i", plain, "-c", "copy", src},
	} {
		cmd, _ := command(context.Background(), false, append([]string{"-v", "error", "-y"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v %s", err, out)
		}
	}
	info, err := Probe(context.Background(), src)
	if err != nil {
		t.Fatal(err)
	}
	v, _ := info.Video()
	if v.Rotation != 90 && v.Rotation != 270 {
		t.Fatalf("rotation %d", v.Rotation)
	}
	f := Frame{W: 640, H: 360, SrcW: v.Width, SrcH: v.Height, Rotation: v.Rotation}
	var want []bool
	for _, mode := range []string{DecodeCPU, DecodeCUDA} {
		img, err := GrabFrame(context.Background(), src, 0.2, f, mode)
		if err != nil && mode == DecodeCUDA {
			t.Logf("no CUDA here: %v", err) // as on CI runners without a GPU
			continue
		}
		if err != nil || img == nil {
			t.Fatalf("%s: %v", mode, err)
		}
		// Upright, the picture is portrait: black bars at the sides, and
		// its halves above and below each other.
		bright := func(x, y int) bool { return img.RGBAAt(x, y).R > 128 }
		got := []bool{bright(20, 180), bright(320, 60), bright(320, 300)}
		if got[0] {
			t.Fatalf("%s: no letterbox at the side: the picture was not turned", mode)
		}
		if want == nil {
			want = got
		} else if got[1] != want[1] || got[2] != want[2] {
			t.Fatalf("%s turned the picture otherwise than ffmpeg's autorotation: %v, %v", mode, got, want)
		}
	}
}
