package media

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"wavealign/internal/dsp"
)

// AudioInput is one file of the external audio, in the order they play.
type AudioInput struct {
	Path   string
	Stream int // absolute stream index
}

// Audio codecs of exported files.
const (
	CodecAAC  = "aac"
	CodecPCM  = "pcm"
	CodecFLAC = "flac"
)

// ExportOptions describe one exported video.
type ExportOptions struct {
	Video  string // the source video, whose video streams are copied
	Output string
	// Inputs are the external audio files, concatenated in order.
	Inputs []AudioInput
	// Offset is where the video's start falls on the external audio, in
	// seconds; negative when the video starts before it.
	Offset float64
	// Drift is how much faster the external audio's clock runs than the
	// video's: the video's second τ plays the external audio at
	// Offset + τ·(1+Drift). The new audio is resampled to follow it.
	Drift float64
	// Duration is the video's, in seconds: the new audio is as long.
	Duration   float64
	SampleRate int
	// Layout is an ffmpeg channel layout ("mono", "stereo", "5.1", "4c"…)
	// and Channels its count of channels.
	Layout   string
	Channels int
	Codec    string // CodecAAC, CodecPCM or CodecFLAC
	Bitrate  string // for AAC, as "320k"
	// KeepOriginal keeps the video's own audio streams after the new one.
	KeepOriginal bool
	Progress     func(float64)
}

// OutputExt returns the extension an output with the codec should have,
// given the one wanted: PCM needs MOV or MKV, FLAC MP4 or MKV.
func OutputExt(want, codec string) string {
	want = strings.ToLower(want)
	switch codec {
	case CodecPCM:
		if want != ".mov" && want != ".mkv" {
			return ".mov"
		}
	case CodecFLAC:
		if want != ".mp4" && want != ".mkv" {
			return ".mkv"
		}
	default:
		if want != ".mp4" && want != ".mov" && want != ".mkv" && want != ".m4v" {
			return ".mp4"
		}
	}
	return want
}

// Layout returns an ffmpeg channel layout for a stream's: its own when it
// has a known one, else the usual one of its count of channels.
func Layout(layout string, channels int) string {
	if layout != "" && layout != "unknown" {
		return layout
	}
	switch channels {
	case 1:
		return "mono"
	case 2:
		return "stereo"
	}
	return strconv.Itoa(max(channels, 1)) + "c"
}

// span returns the frames of the concatenated inputs, at the sample rate,
// that the new audio reads: from to to, with the kernel's reach around.
func (o ExportOptions) span() (from, to int64) {
	sr := float64(o.SampleRate)
	first := o.Offset * sr
	last := (o.Offset + o.Duration*(1+o.Drift)) * sr
	return max(0, int64(math.Floor(first))-dsp.Taps-2), int64(math.Ceil(last)) + dsp.Taps + 2
}

// sourceArgs decode the stretch of the concatenated inputs the new audio
// reads, as raw float samples.
func (o ExportOptions) sourceArgs() []string {
	from, to := o.span()
	args := []string{"-v", "error", "-nostdin"}
	for _, in := range o.Inputs {
		args = append(args, "-i", in.Path)
	}
	var g strings.Builder
	for i, in := range o.Inputs {
		fmt.Fprintf(&g, "[%d:%d]aresample=%d,aformat=sample_fmts=flt:channel_layouts=%s[s%d];", i, in.Stream, o.SampleRate, o.Layout, i)
	}
	src := "s0"
	if len(o.Inputs) > 1 {
		for i := range o.Inputs {
			fmt.Fprintf(&g, "[s%d]", i)
		}
		fmt.Fprintf(&g, "concat=n=%d:v=0:a=1[cat];", len(o.Inputs))
		src = "cat"
	}
	fmt.Fprintf(&g, "[%s]atrim=start_sample=%d:end_sample=%d,asetpts=PTS-STARTPTS[aout]", src, from, to)
	return append(args, "-filter_complex", g.String(), "-map", "[aout]", "-c:a", "pcm_f32le", "-f", "f32le", "pipe:1")
}

// muxArgs put the video's streams, copied, and the new audio from stdin into
// the output.
func (o ExportOptions) muxArgs() []string {
	args := []string{"-y", "-v", "error", "-progress", "pipe:1", "-nostats", "-i", o.Video,
		"-f", "f32le", "-ar", strconv.Itoa(o.SampleRate), "-ch_layout", o.Layout, "-i", "pipe:0",
		"-map", "0:V", "-map", "1:a"}
	if o.KeepOriginal {
		args = append(args, "-map", "0:a?")
	}
	args = append(args, "-c:v", "copy", "-c:a", "copy")
	switch o.Codec {
	case CodecPCM:
		args = append(args, "-c:a:0", "pcm_s24le")
	case CodecFLAC:
		args = append(args, "-c:a:0", "flac")
	default:
		br := o.Bitrate
		if br == "" {
			br = "320k"
		}
		args = append(args, "-c:a:0", "aac", "-b:a:0", br)
	}
	args = append(args,
		"-metadata:s:a:0", "title=WaveAlign",
		"-disposition:a:0", "default",
		"-map_metadata", "0", "-map_chapters", "0")
	if o.KeepOriginal {
		args = append(args, "-disposition:a:1", "0")
	}
	switch strings.ToLower(filepath.Ext(o.Output)) {
	case ".mp4", ".mov", ".m4v":
		args = append(args, "-movflags", "+faststart")
	}
	return append(args, o.Output)
}

// Export writes the video with the external audio in place of its own,
// copying its video streams as they are: nothing of the picture is encoded
// again. One ffmpeg decodes the stretch of the external audio the video
// covers; the new audio is cut from it here, to the fraction of a sample and
// following the drift between the clocks; another ffmpeg encodes it and
// writes the file.
func Export(ctx context.Context, o ExportOptions) error {
	if len(o.Inputs) == 0 {
		return fmt.Errorf("没有外部音频")
	}
	if o.Channels <= 0 {
		return fmt.Errorf("声道数未知")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	src, err := command(ctx, false, o.sourceArgs()...)
	if err != nil {
		return err
	}
	srcOut, err := src.StdoutPipe()
	if err != nil {
		return err
	}
	srcErr, err := src.StderrPipe()
	if err != nil {
		return err
	}
	mux, err := command(ctx, false, o.muxArgs()...)
	if err != nil {
		return err
	}
	muxIn, err := mux.StdinPipe()
	if err != nil {
		return err
	}
	muxOut, err := mux.StdoutPipe()
	if err != nil {
		return err
	}
	muxErr, err := mux.StderrPipe()
	if err != nil {
		return err
	}
	if err := src.Start(); err != nil {
		return err
	}
	if err := mux.Start(); err != nil {
		cancel()
		src.Wait()
		return err
	}
	var srcTail, muxTail, progress tail
	var wg sync.WaitGroup
	wg.Add(3)
	go func() { defer wg.Done(); srcTail.consume(srcErr, nil) }()
	go func() { defer wg.Done(); muxTail.consume(muxErr, nil) }()
	go func() {
		defer wg.Done()
		progress.consume(muxOut, func(line string) {
			k, v, ok := strings.Cut(line, "=")
			if !ok || o.Progress == nil || o.Duration <= 0 {
				return
			}
			if k == "out_time_us" {
				if us, err := strconv.ParseInt(v, 10, 64); err == nil {
					o.Progress(math.Min(float64(us)/1e6/o.Duration, 1))
				}
			}
		})
	}()

	werr := o.resample(bufio.NewReaderSize(srcOut, 1<<20), muxIn)
	cerr := muxIn.Close()
	if werr != nil {
		cancel()
	}
	// What the decoder may still have, after the stretch it was asked for.
	io.Copy(io.Discard, srcOut)
	srcWait := src.Wait()
	muxWait := mux.Wait()
	wg.Wait()
	switch {
	case ctx.Err() != nil && werr == nil:
		return ctx.Err()
	case muxWait != nil:
		return runErr(fmt.Errorf("导出失败"), &muxTail)
	case srcWait != nil:
		return runErr(fmt.Errorf("读取外部音频失败"), &srcTail)
	case werr != nil:
		return werr
	case cerr != nil:
		return cerr
	}
	if o.Progress != nil {
		o.Progress(1)
	}
	return nil
}

// resample writes the new audio to w: Duration seconds at the sample rate,
// frame k taken at Offset + k/rate·(1+Drift) seconds of the external audio,
// which r gives from the first frame of span on.
func (o ExportOptions) resample(r io.Reader, w io.Writer) error {
	c := o.Channels
	sr := float64(o.SampleRate)
	from, _ := o.span()
	total := int64(math.Round(o.Duration * sr))
	x0 := o.Offset*sr - float64(from) // the source frame of output frame 0
	step := 1 + o.Drift

	var buf []float32 // source frames first, first+1, …
	var first int64
	eof := false
	raw := make([]byte, 1<<16*4*c)
	fill := func(need int64) error {
		for !eof && first+int64(len(buf)/c) <= need {
			n, err := io.ReadFull(r, raw)
			n -= n % (4 * c)
			for i := 0; i < n; i += 4 {
				buf = append(buf, math.Float32frombits(binary.LittleEndian.Uint32(raw[i:])))
			}
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				eof = true
			} else if err != nil {
				return err
			}
		}
		return nil
	}
	bw := bufio.NewWriterSize(w, 1<<20)
	frame := make([]float32, c)
	b := make([]byte, 4)
	const block = 4096
	for k := int64(0); k < total; k += block {
		end := min(k+block, total)
		if err := fill(int64(math.Floor(x0+float64(end)*step)) + dsp.Taps + 1); err != nil {
			return err
		}
		for i := k; i < end; i++ {
			dsp.At(buf, c, first, x0+float64(i)*step, frame)
			for _, v := range frame {
				binary.LittleEndian.PutUint32(b, math.Float32bits(v))
				if _, err := bw.Write(b); err != nil {
					return err
				}
			}
		}
		// Drop what no later frame reaches.
		if drop := int64(math.Floor(x0+float64(end)*step)) - dsp.Taps - 1 - first; drop > 1<<18 {
			buf = append(buf[:0], buf[drop*int64(c):]...)
			first += drop
		}
	}
	return bw.Flush()
}
