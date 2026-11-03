package media

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"strconv"
)

// AnalysisRate is the rate audio is decoded at for matching.
const AnalysisRate = 8000

// DecodeMono decodes stream of src to mono float32 at rate: the channels
// mixed (channel 0) or channel alone, counted from 1.
func DecodeMono(ctx context.Context, src string, stream, channel, rate int) ([]float32, error) {
	args := []string{"-v", "error", "-nostdin", "-i", src, "-map", "0:" + strconv.Itoa(stream), "-vn", "-sn", "-dn"}
	if channel > 0 {
		args = append(args, "-af", fmt.Sprintf("pan=mono|c0=c%d", channel-1))
	} else {
		args = append(args, "-ac", "1")
	}
	args = append(args, "-ar", strconv.Itoa(rate), "-c:a", "pcm_f32le", "-f", "f32le", "pipe:1")
	cmd, err := command(ctx, false, args...)
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	var t tail
	go t.consume(stderr, nil)
	var data bytes.Buffer
	_, cerr := io.Copy(&data, stdout)
	if err := cmd.Wait(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, runErr(fmt.Errorf("解码分析音频失败"), &t)
	}
	if cerr != nil {
		return nil, cerr
	}
	raw := data.Bytes()
	out := make([]float32, len(raw)/4)
	for i := range out {
		out[i] = math.Float32frombits(binary.LittleEndian.Uint32(raw[4*i:]))
	}
	return out, nil
}
