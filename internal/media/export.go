package media

import (
	"context"
	"fmt"
	"math"
	"path/filepath"
	"strconv"
	"strings"
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
	// Duration is the video's, in seconds: the new audio is as long.
	Duration   float64
	SampleRate int
	// Layout is an ffmpeg channel layout: "mono", "stereo", "5.1", "4c"…
	Layout  string
	Codec   string // CodecAAC, CodecPCM or CodecFLAC
	Bitrate string // for AAC, as "320k"
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

// FilterGraph returns the filter graph that cuts the video's stretch out of
// the concatenated inputs, as [aout]; inputs are ffmpeg inputs 1 to n.
func (o ExportOptions) FilterGraph() string {
	sr := o.SampleRate
	var g strings.Builder
	for i, in := range o.Inputs {
		fmt.Fprintf(&g, "[%d:%d]aresample=%d,aformat=sample_fmts=fltp:channel_layouts=%s[s%d];", i+1, in.Stream, sr, o.Layout, i)
	}
	src := "s0"
	if len(o.Inputs) > 1 {
		for i := range o.Inputs {
			fmt.Fprintf(&g, "[s%d]", i)
		}
		fmt.Fprintf(&g, "concat=n=%d:v=0:a=1[cat];", len(o.Inputs))
		src = "cat"
	}
	start := int64(math.Round(o.Offset * float64(sr)))
	length := int64(math.Round(o.Duration * float64(sr)))
	fmt.Fprintf(&g, "[%s]", src)
	pre := int64(0)
	if start < 0 {
		pre = -start
		fmt.Fprintf(&g, "adelay=delays=%dS:all=1,", pre)
	}
	s := start + pre
	fmt.Fprintf(&g, "atrim=start_sample=%d:end_sample=%d,asetpts=PTS-STARTPTS,apad=whole_len=%d[aout]", s, s+length, length)
	return g.String()
}

// Args returns ffmpeg's arguments for the export.
func (o ExportOptions) Args() []string {
	args := []string{"-y", "-v", "error", "-nostdin", "-progress", "pipe:1", "-nostats", "-i", o.Video}
	for _, in := range o.Inputs {
		args = append(args, "-i", in.Path)
	}
	args = append(args, "-filter_complex", o.FilterGraph(), "-map", "0:V", "-map", "[aout]")
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
// again.
func Export(ctx context.Context, o ExportOptions) error {
	if len(o.Inputs) == 0 {
		return fmt.Errorf("没有外部音频")
	}
	cmd, err := command(ctx, false, o.Args()...)
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
	var p tail
	p.consume(stdout, func(line string) {
		k, v, ok := strings.Cut(line, "=")
		if !ok || o.Progress == nil || o.Duration <= 0 {
			return
		}
		switch k {
		case "out_time_us":
			if us, err := strconv.ParseInt(v, 10, 64); err == nil {
				o.Progress(math.Min(float64(us)/1e6/o.Duration, 1))
			}
		case "progress":
			if v == "end" {
				o.Progress(1)
			}
		}
	})
	if err := cmd.Wait(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return runErr(fmt.Errorf("导出失败"), &t)
	}
	return nil
}
