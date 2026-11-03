package media

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"wavealign/internal/wave"
)

// PlayRate is the rate of the decoded audio the app plays and draws: 48 kHz,
// stereo, 16 bits.
const PlayRate = 48000

// PCM is a stream's audio decoded to a cache file: 16-bit stereo at PlayRate.
type PCM struct {
	Path   string
	Frames int64
	f      *os.File
}

// Seconds returns how long the audio is.
func (p *PCM) Seconds() float64 { return float64(p.Frames) / PlayRate }

// Read fills dst, interleaved stereo float32, with the frames from frame on,
// zeros outside the audio.
func (p *PCM) Read(dst []float32, frame int64) {
	n := int64(len(dst) / 2)
	clear(dst)
	from, to := max(frame, 0), min(frame+n, p.Frames)
	if to <= from {
		return
	}
	buf := make([]byte, (to-from)*4)
	got, _ := p.f.ReadAt(buf, from*4)
	off := int(from-frame) * 2
	for i := 0; i+1 < got; i += 2 {
		dst[off+i/2] = float32(int16(binary.LittleEndian.Uint16(buf[i:]))) / 32768
	}
}

// ReadMono fills dst with frames from frame on, mixed to mono (channel 0),
// or the left (1) or right (2) channel alone.
func (p *PCM) ReadMono(dst []float32, frame int64, channel int) {
	st := make([]float32, 2*len(dst))
	p.Read(st, frame)
	for i := range dst {
		l, r := st[2*i], st[2*i+1]
		switch channel {
		case 1:
			dst[i] = l
		case 2:
			dst[i] = r
		default:
			dst[i] = (l + r) / 2
		}
	}
}

// Close closes the cache file.
func (p *PCM) Close() error {
	if p == nil || p.f == nil {
		return nil
	}
	return p.f.Close()
}

// CacheDir is where decoded audio is kept.
func CacheDir() string {
	d, err := os.UserCacheDir()
	if err != nil {
		d = os.TempDir()
	}
	return filepath.Join(d, "WaveAlign", "pcm")
}

// ClearCache deletes the decoded audio, except that of the cache files in
// keep (PCM.Path values).
func ClearCache(keep map[string]bool) (int64, error) {
	dir := CacheDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, nil
	}
	var freed int64
	for _, e := range entries {
		p := filepath.Join(dir, e.Name())
		if keep[strings.TrimSuffix(p, filepath.Ext(p))+".pcm"] {
			continue
		}
		if info, err := e.Info(); err == nil {
			if os.Remove(p) == nil {
				freed += info.Size()
			}
		}
	}
	return freed, nil
}

func cacheKey(src string, stream int) (string, error) {
	abs, err := filepath.Abs(src)
	if err != nil {
		return "", err
	}
	st, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	h := sha1.Sum([]byte(abs + "|" + strconv.FormatInt(st.Size(), 10) + "|" +
		strconv.FormatInt(st.ModTime().UnixNano(), 10) + "|" + strconv.Itoa(stream) + "|pcm1"))
	return hex.EncodeToString(h[:]), nil
}

// DecodePCM decodes stream of src into the cache, unless it is there, and
// returns it with its peaks. duration, in seconds, scales progress.
func DecodePCM(ctx context.Context, src string, stream int, duration float64, progress func(float64)) (*PCM, *wave.Peaks, error) {
	key, err := cacheKey(src, stream)
	if err != nil {
		return nil, nil, err
	}
	dir := CacheDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, nil, err
	}
	pcmPath := filepath.Join(dir, key+".pcm")
	peaksPath := filepath.Join(dir, key+".peaks")
	if pcm, peaks, err := openCached(pcmPath, peaksPath); err == nil {
		return pcm, peaks, nil
	}

	cmd, err := command(ctx, false, "-v", "error", "-nostdin", "-i", src,
		"-map", "0:"+strconv.Itoa(stream), "-vn", "-sn", "-dn",
		"-ac", "2", "-ar", strconv.Itoa(PlayRate), "-c:a", "pcm_s16le", "-f", "s16le", "pipe:1")
	if err != nil {
		return nil, nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, nil, err
	}
	tmp, err := os.CreateTemp(dir, key+"-*.tmp")
	if err != nil {
		return nil, nil, err
	}
	defer os.Remove(tmp.Name())
	if err := cmd.Start(); err != nil {
		tmp.Close()
		return nil, nil, err
	}
	var t tail
	go t.consume(stderr, nil)

	b := wave.NewBuilder(PlayRate, 2)
	w := bufio.NewWriterSize(tmp, 1<<20)
	buf := make([]byte, 256*1024)
	samples := make([]int16, len(buf)/2)
	var frames int64
	var carry []byte
	total := duration * PlayRate
	for {
		n, rerr := stdout.Read(buf[len(carry):])
		if len(carry) > 0 {
			copy(buf, carry)
		}
		data := buf[:len(carry)+n]
		whole := len(data) / 4 * 4
		if _, err := w.Write(data[:whole]); err != nil {
			tmp.Close()
			cmd.Process.Kill()
			cmd.Wait()
			return nil, nil, err
		}
		s := samples[:whole/2]
		for i := range s {
			s[i] = int16(binary.LittleEndian.Uint16(data[2*i:]))
		}
		b.Add(s)
		frames += int64(whole / 4)
		carry = append(carry[:0], data[whole:]...)
		if progress != nil && total > 0 {
			progress(math.Min(float64(frames)/total, 1))
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			tmp.Close()
			cmd.Process.Kill()
			cmd.Wait()
			return nil, nil, rerr
		}
	}
	werr := w.Flush()
	cerr := tmp.Close()
	if err := cmd.Wait(); err != nil {
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		return nil, nil, runErr(fmt.Errorf("解码音频失败"), &t)
	}
	if werr != nil || cerr != nil {
		return nil, nil, fmt.Errorf("写入缓存失败: %v %v", werr, cerr)
	}
	if frames == 0 {
		return nil, nil, fmt.Errorf("%s 中没有可解码的音频", filepath.Base(src))
	}
	peaks := b.Finish()
	if err := os.Rename(tmp.Name(), pcmPath); err != nil {
		return nil, nil, err
	}
	if pf, err := os.Create(peaksPath); err == nil {
		bw := bufio.NewWriter(pf)
		peaks.Write(bw)
		bw.Flush()
		pf.Close()
	}
	f, err := os.Open(pcmPath)
	if err != nil {
		return nil, nil, err
	}
	return &PCM{Path: pcmPath, Frames: frames, f: f}, peaks, nil
}

func openCached(pcmPath, peaksPath string) (*PCM, *wave.Peaks, error) {
	st, err := os.Stat(pcmPath)
	if err != nil {
		return nil, nil, err
	}
	pf, err := os.Open(peaksPath)
	if err != nil {
		return nil, nil, err
	}
	peaks, err := wave.Read(bufio.NewReader(pf))
	pf.Close()
	if err != nil || peaks.Frames != st.Size()/4 {
		return nil, nil, fmt.Errorf("stale cache")
	}
	f, err := os.Open(pcmPath)
	if err != nil {
		return nil, nil, err
	}
	return &PCM{Path: pcmPath, Frames: st.Size() / 4, f: f}, peaks, nil
}
