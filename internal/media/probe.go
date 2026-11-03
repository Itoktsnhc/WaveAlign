package media

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

// Stream is one stream of a file.
type Stream struct {
	Index      int    // among all of the file's streams, for -map 0:Index
	Type       string // "audio", "video", "subtitle", "data"
	Codec      string
	Channels   int
	Layout     string
	SampleRate int
	StartTime  float64
	Duration   float64
	Language   string
	Title      string
	Width      int
	Height     int
	FPS        float64
	Picture    bool // an attached picture, as cover art
}

// Info is what ffprobe says about a file.
type Info struct {
	Path      string
	Format    string
	Duration  float64
	StartTime float64
	Streams   []Stream
}

// Audio returns the audio streams.
func (i *Info) Audio() []Stream {
	var out []Stream
	for _, s := range i.Streams {
		if s.Type == "audio" {
			out = append(out, s)
		}
	}
	return out
}

// Video returns the first video stream that is not a picture, if any.
func (i *Info) Video() (Stream, bool) {
	for _, s := range i.Streams {
		if s.Type == "video" && !s.Picture {
			return s, true
		}
	}
	return Stream{}, false
}

// Label describes an audio stream for a menu; n counts from 0.
func (s Stream) Label(n int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "音轨 %d · %s", n+1, s.Codec)
	switch s.Channels {
	case 1:
		b.WriteString(" · 单声道")
	case 2:
		b.WriteString(" · 立体声")
	default:
		fmt.Fprintf(&b, " · %d 声道", s.Channels)
		if s.Layout != "" {
			fmt.Fprintf(&b, " (%s)", s.Layout)
		}
	}
	if s.SampleRate > 0 {
		fmt.Fprintf(&b, " · %g kHz", float64(s.SampleRate)/1000)
	}
	if s.Language != "" && s.Language != "und" {
		b.WriteString(" · " + s.Language)
	}
	if s.Title != "" {
		b.WriteString(" · " + s.Title)
	}
	return b.String()
}

// Probe reads what a file holds.
func Probe(ctx context.Context, path string) (*Info, error) {
	cmd, err := command(ctx, true, "-v", "error", "-print_format", "json", "-show_format", "-show_streams", path)
	if err != nil {
		return nil, err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("无法读取 %s: %s", filepath.Base(path), msg)
	}
	var raw struct {
		Streams []struct {
			Index         int    `json:"index"`
			CodecType     string `json:"codec_type"`
			CodecName     string `json:"codec_name"`
			Channels      int    `json:"channels"`
			ChannelLayout string `json:"channel_layout"`
			SampleRate    string `json:"sample_rate"`
			StartTime     string `json:"start_time"`
			Duration      string `json:"duration"`
			Width         int    `json:"width"`
			Height        int    `json:"height"`
			AvgFrameRate  string `json:"avg_frame_rate"`
			RFrameRate    string `json:"r_frame_rate"`
			Disposition   struct {
				AttachedPic int `json:"attached_pic"`
			} `json:"disposition"`
			Tags map[string]string `json:"tags"`
		} `json:"streams"`
		Format struct {
			FormatName string `json:"format_name"`
			Duration   string `json:"duration"`
			StartTime  string `json:"start_time"`
		} `json:"format"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, err
	}
	info := &Info{
		Path:      path,
		Format:    raw.Format.FormatName,
		Duration:  num(raw.Format.Duration),
		StartTime: num(raw.Format.StartTime),
	}
	for _, s := range raw.Streams {
		st := Stream{
			Index:      s.Index,
			Type:       s.CodecType,
			Codec:      s.CodecName,
			Channels:   s.Channels,
			Layout:     s.ChannelLayout,
			SampleRate: int(num(s.SampleRate)),
			StartTime:  num(s.StartTime),
			Duration:   num(s.Duration),
			Width:      s.Width,
			Height:     s.Height,
			FPS:        rational(s.AvgFrameRate),
			Picture:    s.Disposition.AttachedPic != 0,
			Language:   s.Tags["language"],
			Title:      s.Tags["title"],
		}
		if st.FPS == 0 {
			st.FPS = rational(s.RFrameRate)
		}
		info.Streams = append(info.Streams, st)
	}
	if info.Duration == 0 {
		for _, s := range info.Streams {
			info.Duration = max(info.Duration, s.StartTime-info.StartTime+s.Duration)
		}
	}
	return info, nil
}

func num(s string) float64 {
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return v
}

func rational(s string) float64 {
	a, b, ok := strings.Cut(s, "/")
	if !ok {
		return num(s)
	}
	d := num(b)
	if d == 0 {
		return 0
	}
	return num(a) / d
}
