package media

import (
	"context"
	"fmt"
	"image"
	"io"
	"strconv"
)

// frameArgs are the arguments that decode src's first video stream from t
// on, scaled and letterboxed into w×h RGBA frames.
func frameArgs(src string, t float64, w, h int, fps float64, hw string) []string {
	args := []string{"-v", "error", "-nostdin"}
	args = append(args, hwaccelArgs(hw)...)
	if t > 0 {
		args = append(args, "-ss", strconv.FormatFloat(t, 'f', 4, 64))
	}
	vf := fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=decrease,pad=%d:%d:(ow-iw)/2:(oh-ih)/2,format=rgba", w, h, w, h)
	if fps > 0 {
		vf = "fps=" + strconv.FormatFloat(fps, 'f', 3, 64) + "," + vf
	}
	return append(args, "-i", src, "-map", "0:v:0", "-an", "-sn", "-dn", "-vf", vf, "-f", "rawvideo", "pipe:1")
}

// GrabFrame decodes the frame of src at t seconds from its start, scaled into
// w×h; nil when there is none there.
func GrabFrame(ctx context.Context, src string, t float64, w, h int, hw string) (*image.RGBA, error) {
	args := frameArgs(src, t, w, h, 0, hw)
	args = append(args[:len(args)-1], "-frames:v", "1", "pipe:1")
	var img *image.RGBA
	err := runFrames(ctx, args, w, h, func(f *image.RGBA) bool {
		img = f
		return false
	})
	return img, err
}

// StreamFrames decodes src from t seconds on at fps frames a second, scaled
// into w×h, and gives each to each, a new image every time, until each
// returns false, ctx ends or the video does.
func StreamFrames(ctx context.Context, src string, t float64, w, h int, fps float64, hw string, each func(*image.RGBA) bool) error {
	return runFrames(ctx, frameArgs(src, t, w, h, fps, hw), w, h, each)
}

func runFrames(ctx context.Context, args []string, w, h int, each func(*image.RGBA) bool) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd, err := command(ctx, false, args...)
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	var t tail
	go t.consume(stderr, nil)
	stopped := false
	for {
		img := image.NewRGBA(image.Rect(0, 0, w, h))
		if _, err := io.ReadFull(stdout, img.Pix); err != nil {
			break
		}
		if !each(img) {
			stopped = true
			cancel()
			break
		}
	}
	err = cmd.Wait()
	if stopped || ctx.Err() != nil {
		return nil
	}
	return runErr(err, &t)
}
