// Package media runs ffmpeg and ffprobe: it reads what files hold, decodes
// their audio for analysis and playback, grabs preview frames and writes the
// exported videos.
package media

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

var (
	toolsMu sync.RWMutex
	ffmpeg  string
	ffprobe string
)

// ErrNoFFmpeg says ffmpeg or ffprobe was not found.
var ErrNoFFmpeg = errors.New("找不到 ffmpeg / ffprobe：请把它们放在程序目录、加入 PATH，或在设置中指定目录")

// Configure finds ffmpeg and ffprobe: in dir when given, else beside the
// executable, else on the PATH.
func Configure(dir string) error {
	exe := func(name string) string {
		if runtime.GOOS == "windows" {
			return name + ".exe"
		}
		return name
	}
	var dirs []string
	if dir != "" {
		dirs = append(dirs, dir, filepath.Join(dir, "bin"))
	}
	if self, err := os.Executable(); err == nil {
		d := filepath.Dir(self)
		dirs = append(dirs, d, filepath.Join(d, "ffmpeg"), filepath.Join(d, "ffmpeg", "bin"))
	}
	find := func(name string) string {
		for _, d := range dirs {
			p := filepath.Join(d, exe(name))
			if st, err := os.Stat(p); err == nil && !st.IsDir() {
				return p
			}
		}
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
		return ""
	}
	mp, pp := find("ffmpeg"), find("ffprobe")
	toolsMu.Lock()
	ffmpeg, ffprobe = mp, pp
	toolsMu.Unlock()
	if mp == "" || pp == "" {
		return ErrNoFFmpeg
	}
	return nil
}

// Tools returns the paths found by Configure.
func Tools() (ffmpegPath, ffprobePath string) {
	toolsMu.RLock()
	defer toolsMu.RUnlock()
	return ffmpeg, ffprobe
}

func command(ctx context.Context, probe bool, args ...string) (*exec.Cmd, error) {
	mp, pp := Tools()
	bin := mp
	if probe {
		bin = pp
	}
	if bin == "" {
		return nil, ErrNoFFmpeg
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	hideWindow(cmd)
	return cmd, nil
}

// tail keeps the last lines written to it, for error messages.
type tail struct {
	mu    sync.Mutex
	lines []string
}

func (t *tail) consume(r io.Reader, each func(line string)) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	sc.Split(scanLinesCR)
	for sc.Scan() {
		line := sc.Text()
		if each != nil {
			each(line)
		}
		t.mu.Lock()
		t.lines = append(t.lines, line)
		if len(t.lines) > 40 {
			t.lines = t.lines[len(t.lines)-40:]
		}
		t.mu.Unlock()
	}
}

func (t *tail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	var keep []string
	for _, l := range t.lines {
		l = strings.TrimSpace(l)
		if l == "" || strings.Count(l, "=") > 0 && !strings.Contains(l, " ") {
			continue // progress key=value lines
		}
		keep = append(keep, l)
	}
	if len(keep) > 4 {
		keep = keep[len(keep)-4:]
	}
	return strings.Join(keep, "\n")
}

// scanLinesCR splits on \n and \r, as ffmpeg's progress lines end in \r.
func scanLinesCR(data []byte, atEOF bool) (int, []byte, error) {
	for i, b := range data {
		if b == '\n' || b == '\r' {
			return i + 1, data[:i], nil
		}
	}
	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}

// runErr wraps a failed run with what ffmpeg said last.
func runErr(err error, t *tail) error {
	if err == nil {
		return nil
	}
	if msg := t.String(); msg != "" {
		return fmt.Errorf("%w: %s", err, msg)
	}
	return err
}

// hwaccelArgs turns a decoding preference into ffmpeg input options.
func hwaccelArgs(mode string) []string {
	switch mode {
	case "", "none":
		return nil
	default:
		return []string{"-hwaccel", mode}
	}
}
