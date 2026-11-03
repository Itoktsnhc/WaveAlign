package media

import (
	"context"
	"math"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"wavealign/internal/align"
)

func ff(t *testing.T, args ...string) {
	t.Helper()
	mp, _ := Tools()
	out, err := exec.Command(mp, append([]string{"-v", "error", "-y"}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("ffmpeg %v: %v\n%s", args, err, out)
	}
}

// TestPipeline makes a recorder's audio split over two files and a camera
// clip that heard part of it, finds the clip's place, exports it, and checks
// the picture was copied and the new sound is where it belongs.
func TestPipeline(t *testing.T) {
	if err := Configure(""); err != nil {
		t.Skip(err)
	}
	ctx := context.Background()
	dir := t.TempDir()
	t.Setenv("LOCALAPPDATA", dir) // keep the cache out of the user's
	master := filepath.Join(dir, "master.wav")
	ff(t, "-f", "lavfi", "-i", "anoisesrc=d=45:c=pink:r=48000:a=0.5:seed=7",
		"-af", "volume='if(gt(sin(2*PI*t*1.7)+sin(2*PI*t*0.53),0.3),1,0.05)':eval=frame", "-ac", "2", master)
	a1 := filepath.Join(dir, "a1.wav")
	a2 := filepath.Join(dir, "a2.mp3")
	ff(t, "-i", master, "-t", "20", "-ar", "44100", a1)
	ff(t, "-ss", "20", "-i", master, "-ac", "1", "-b:a", "192k", a2)
	clip := filepath.Join(dir, "clip.mp4")
	const want = 17.0
	ff(t, "-f", "lavfi", "-i", "testsrc2=d=15:s=320x240:r=30",
		"-ss", "17", "-t", "15", "-i", master,
		"-f", "lavfi", "-i", "sine=f=440:d=15",
		"-map", "0:v", "-map", "1:a", "-map", "2:a",
		"-af:a:0", "lowpass=f=3000,volume=0.3",
		"-c:v", "libx264", "-preset", "ultrafast", "-c:a", "aac", "-shortest", clip)

	var ext []float32
	var inputs []AudioInput
	for _, p := range []string{a1, a2} {
		info, err := Probe(ctx, p)
		if err != nil {
			t.Fatal(err)
		}
		s := info.Audio()[0]
		x, err := DecodeMono(ctx, p, s.Index, 0, AnalysisRate)
		if err != nil {
			t.Fatal(err)
		}
		ext = append(ext, x...)
		inputs = append(inputs, AudioInput{Path: p, Stream: s.Index})
	}
	info, err := Probe(ctx, clip)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(info.Audio()); n != 2 {
		t.Fatalf("%d audio streams", n)
	}
	as := info.Audio()[0]
	q, err := DecodeMono(ctx, clip, as.Index, 1, AnalysisRate)
	if err != nil {
		t.Fatal(err)
	}
	res, err := align.Find(ext, q, align.Options{Rate: AnalysisRate})
	if err != nil {
		t.Fatal(err)
	}
	offset := res.Lag - (as.StartTime - info.StartTime)
	if math.Abs(offset-want) > 0.002 {
		t.Fatalf("offset %.4f, want %.4f (conf %.2f)", offset, want, res.Confidence)
	}
	t.Logf("offset %.5f conf %.2f", offset, res.Confidence)

	pcm, peaks, err := DecodePCM(ctx, clip, as.Index, info.Duration, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer pcm.Close()
	if math.Abs(pcm.Seconds()-15) > 0.1 || peaks.Frames != pcm.Frames {
		t.Fatalf("pcm %.3f s, peaks %d frames", pcm.Seconds(), peaks.Frames)
	}

	for _, codec := range []string{CodecAAC, CodecPCM} {
		out := filepath.Join(dir, "out-"+codec+OutputExt(".mp4", codec))
		err = Export(ctx, ExportOptions{
			Video: clip, Output: out, Inputs: inputs, Offset: offset, Duration: info.Duration,
			SampleRate: 48000, Layout: "stereo", Channels: 2, Codec: codec, KeepOriginal: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		oi, err := Probe(ctx, out)
		if err != nil {
			t.Fatal(err)
		}
		if n := len(oi.Audio()); n != 3 {
			t.Fatalf("%s: %d audio streams, want the new one and both originals", codec, n)
		}
		if md5(t, clip) != md5(t, out) {
			t.Fatalf("%s: the video stream changed", codec)
		}
		// The new audio, found in the master, sits at the offset.
		got, err := DecodeMono(ctx, out, oi.Audio()[0].Index, 0, 48000)
		if err != nil {
			t.Fatal(err)
		}
		m, err := DecodeMono(ctx, master, 0, 0, 48000)
		if err != nil {
			t.Fatal(err)
		}
		from := int((want - 1) * 48000)
		pos, conf := align.Locate(m[from:from+12*48000], got[48000:48000*10], 0)
		at := float64(from+int(pos))/48000 - 1
		if math.Abs(at-want) > 0.001 {
			t.Fatalf("%s: exported audio starts at %.5f in the master, want %.5f (conf %.2f)", codec, at, want, conf)
		}
		if d := float64(len(got)) / 48000; math.Abs(d-info.Duration) > 0.05 {
			t.Fatalf("%s: exported audio lasts %.3f s, video %.3f s", codec, d, info.Duration)
		}
		t.Logf("%s: exported audio at %.5f s", codec, at)
	}

	// A video starting before the recorder: the new audio begins with silence.
	out := filepath.Join(dir, "early.mp4")
	err = Export(ctx, ExportOptions{
		Video: clip, Output: out, Inputs: inputs, Offset: -2, Duration: info.Duration,
		SampleRate: 48000, Layout: "mono", Channels: 1, Codec: CodecAAC,
	})
	if err != nil {
		t.Fatal(err)
	}
	oi, _ := Probe(ctx, out)
	got, _ := DecodeMono(ctx, out, oi.Audio()[0].Index, 0, 48000)
	quiet := 0.0
	for _, v := range got[:48000] {
		quiet = math.Max(quiet, math.Abs(float64(v)))
	}
	if quiet > 0.01 || oi.Audio()[0].Channels != 1 {
		t.Fatalf("first second peaks at %.3f, %d channels", quiet, oi.Audio()[0].Channels)
	}
}

func md5(t *testing.T, path string) string {
	t.Helper()
	mp, _ := Tools()
	out, err := exec.Command(mp, "-v", "error", "-i", path, "-map", "0:v", "-c", "copy", "-f", "md5", "-").Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

func TestOutputExt(t *testing.T) {
	cases := map[[2]string]string{
		{".mp4", CodecAAC}: ".mp4", {".mov", CodecPCM}: ".mov", {".mp4", CodecPCM}: ".mov",
		{".mov", CodecFLAC}: ".mkv", {".avi", CodecAAC}: ".mp4",
	}
	for in, want := range cases {
		if got := OutputExt(in[0], in[1]); got != want {
			t.Errorf("OutputExt(%q, %q) = %q, want %q", in[0], in[1], got, want)
		}
	}

}
