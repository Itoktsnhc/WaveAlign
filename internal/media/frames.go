package media

import (
	"context"
	"fmt"
	"image"
	"io"
	"strconv"
)

// Decoders of preview frames, fastest first.
const (
	// DecodeCUDA decodes with NVDEC and scales with CUDA: only the small
	// frames leave the GPU.
	DecodeCUDA = "cuda"
	// DecodeD3D11 decodes with Direct3D 11 Video and scales on the CPU.
	DecodeD3D11 = "d3d11va"
	// DecodeCPU decodes and scales on the CPU.
	DecodeCPU = "none"
)

// DecodeChain returns the decoders to try for a preference: "auto" tries
// them all, fastest first, and each falls back to the CPU.
func DecodeChain(pref string) []string {
	switch pref {
	case DecodeCUDA:
		return []string{DecodeCUDA, DecodeCPU}
	case DecodeD3D11:
		return []string{DecodeD3D11, DecodeCPU}
	case DecodeCPU:
		return []string{DecodeCPU}
	default:
		return []string{DecodeCUDA, DecodeD3D11, DecodeCPU}
	}
}

// DecodeLabel names a decoder for the user.
func DecodeLabel(mode string) string {
	switch mode {
	case DecodeCUDA:
		return "GPU（NVDEC 解码 + CUDA 缩放）"
	case DecodeD3D11:
		return "GPU（D3D11 解码）+ CPU 缩放"
	case DecodeCPU:
		return "CPU"
	}
	return "自动"
}

// Frame describes the preview frames: w×h RGBA, the picture of a stream of
// the given size and rotation fitted inside and letterboxed.
type Frame struct {
	W, H int
	// The video stream's coded size and rotation.
	SrcW, SrcH, Rotation int
}

// fit returns the size to scale the coded picture to, before it is turned,
// and the filter that turns it upright.
func (f Frame) fit() (sw, sh int, turn string) {
	dw, dh := f.SrcW, f.SrcH
	switch f.Rotation {
	case 90:
		turn = "transpose=clock"
		dw, dh = dh, dw
	case 180:
		turn = "hflip,vflip"
	case 270:
		turn = "transpose=cclock"
		dw, dh = dh, dw
	}
	if dw <= 0 || dh <= 0 {
		dw, dh = f.W, f.H
	}
	s := min(float64(f.W)/float64(dw), float64(f.H)/float64(dh))
	ow := max(int(float64(dw)*s)/2*2, 2)
	oh := max(int(float64(dh)*s)/2*2, 2)
	if f.Rotation == 90 || f.Rotation == 270 {
		return oh, ow, turn
	}
	return ow, oh, turn
}

// frameArgs are the arguments that decode src's first video stream from t
// on into f's frames, fps a second (0 for every frame), with mode.
func frameArgs(src string, t float64, f Frame, fps float64, mode string) []string {
	args := []string{"-v", "error", "-nostdin"}
	pad := fmt.Sprintf("pad=%d:%d:(ow-iw)/2:(oh-ih)/2,format=rgba", f.W, f.H)
	var vf string
	switch mode {
	case DecodeCUDA:
		// Decoded and scaled on the GPU; the picture is turned upright
		// once small, on the CPU, as autorotation cannot take GPU frames.
		args = append(args, "-hwaccel", "cuda", "-hwaccel_output_format", "cuda", "-noautorotate")
		sw, sh, turn := f.fit()
		vf = fmt.Sprintf("scale_cuda=w=%d:h=%d:format=nv12,hwdownload,format=nv12", sw, sh)
		if turn != "" {
			vf += "," + turn
		}
		vf += "," + pad
	default:
		if mode == DecodeD3D11 {
			args = append(args, "-hwaccel", "d3d11va")
		}
		vf = fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=decrease,%s", f.W, f.H, pad)
	}
	if fps > 0 {
		vf = "fps=" + strconv.FormatFloat(fps, 'f', 3, 64) + "," + vf
	}
	if t > 0 {
		args = append(args, "-ss", strconv.FormatFloat(t, 'f', 4, 64))
	}
	return append(args, "-i", src, "-map", "0:v:0", "-an", "-sn", "-dn", "-vf", vf, "-f", "rawvideo", "pipe:1")
}

// GrabFrame decodes the frame of src at t seconds from its start; nil when
// there is none there.
func GrabFrame(ctx context.Context, src string, t float64, f Frame, mode string) (*image.RGBA, error) {
	args := frameArgs(src, t, f, 0, mode)
	args = append(args[:len(args)-1], "-frames:v", "1", "pipe:1")
	var img *image.RGBA
	_, err := runFrames(ctx, args, f, func(i *image.RGBA) bool {
		img = i
		return false
	})
	return img, err
}

// StreamFrames decodes src from t seconds on at fps frames a second and gives
// each to each, a new image every time, until each returns false, ctx ends
// or the video does. It returns how many frames it gave.
func StreamFrames(ctx context.Context, src string, t float64, f Frame, fps float64, mode string, each func(*image.RGBA) bool) (int, error) {
	return runFrames(ctx, frameArgs(src, t, f, fps, mode), f, each)
}

func runFrames(ctx context.Context, args []string, f Frame, each func(*image.RGBA) bool) (int, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd, err := command(ctx, false, args...)
	if err != nil {
		return 0, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return 0, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return 0, err
	}
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	var t tail
	go t.consume(stderr, nil)
	n := 0
	stopped := false
	for {
		img := image.NewRGBA(image.Rect(0, 0, f.W, f.H))
		if _, err := io.ReadFull(stdout, img.Pix); err != nil {
			break
		}
		n++
		if !each(img) {
			stopped = true
			cancel()
			break
		}
	}
	err = cmd.Wait()
	if stopped || ctx.Err() != nil {
		return n, nil
	}
	return n, runErr(err, &t)
}
