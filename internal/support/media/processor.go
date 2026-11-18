// Package media prepares bounded private audio/video files in worker jobs.
package media

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"time"
)

type Processor struct{ Binary string }

func (p Processor) command(ctx context.Context, binary string, args ...string) *exec.Cmd {
	if runtime.GOOS == "linux" {
		args = append([]string{"--as=2147483648", "--cpu=120", "--fsize=314572800", "--", binary}, args...)
		binary = "prlimit"
	}
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd
}
func (p Processor) Prepare(ctx context.Context, kind, source string, maxBytes int64) (path, mime string, duration float64, width, height int, err error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	probe := filepath.Join(filepath.Dir(p.Binary), "ffprobe")
	if filepath.Base(p.Binary) == p.Binary {
		probe = "ffprobe"
	}
	args := []string{"-v", "error", "-max_alloc", "67108864", "-protocol_whitelist", "file,pipe", "-format_whitelist", "mov,matroska,webm,mp3,ogg,wav,flac,aac,avi,asf", "-show_entries", "format=duration:stream=codec_type,width,height", "-of", "json", source}
	// Probe output contains only the explicitly selected numeric/codec fields.
	cmd := p.command(ctx, probe, args...)
	output, e := cmd.Output()
	if e != nil || len(output) > 65536 {
		return "", "", 0, 0, 0, errors.New("media metadata unavailable")
	}
	var meta struct {
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
		Streams []struct {
			Type   string `json:"codec_type"`
			Width  int    `json:"width"`
			Height int    `json:"height"`
		} `json:"streams"`
	}
	if json.Unmarshal(output, &meta) != nil {
		return "", "", 0, 0, 0, errors.New("invalid media metadata")
	}
	duration, _ = strconv.ParseFloat(meta.Format.Duration, 64)
	hasAudio, hasVideo := false, false
	for _, s := range meta.Streams {
		if s.Type == "audio" {
			hasAudio = true
		}
		if s.Type == "video" {
			hasVideo = true
			width = s.Width
			height = s.Height
		}
	}
	// Streaming MediaRecorder WebM commonly omits duration. Bound decoding by
	// CPU/time/output size, then validate the finalized output duration below.
	if duration > 3600 || (kind == "audio" && !hasAudio) || (kind == "video" && !hasVideo) || width > 7680 || height > 7680 {
		return "", "", 0, 0, 0, errors.New("media outside limits")
	}
	ext := ".mp3"
	mime = "audio/mpeg"
	if kind == "video" {
		ext = ".mp4"
		mime = "video/mp4"
	}
	f, e := os.CreateTemp("", "support-ready-*"+ext)
	if e != nil {
		return "", "", 0, 0, 0, errors.New("media staging unavailable")
	}
	path = f.Name()
	f.Close()
	args = []string{"-v", "error", "-nostdin", "-y", "-max_alloc", "67108864", "-protocol_whitelist", "file,pipe", "-format_whitelist", "mov,matroska,webm,mp3,ogg,wav,flac,aac,avi,asf", "-threads", "1", "-i", source, "-map_metadata", "-1", "-threads", "1", "-filter_threads", "1", "-fs", strconv.FormatInt(maxBytes, 10)}
	if kind == "audio" {
		args = append(args, "-vn", "-c:a", "libmp3lame", "-b:a", "128k")
	} else {
		args = append(args, "-vf", "scale='trunc(min(1280,iw)/2)*2':-2", "-c:v", "libx264", "-preset", "fast", "-crf", "25", "-pix_fmt", "yuv420p", "-c:a", "aac", "-movflags", "+faststart")
	}
	args = append(args, path)
	if e = p.command(ctx, p.Binary, args...).Run(); e != nil {
		os.Remove(path)
		return "", "", 0, 0, 0, errors.New("media preparation failed")
	}
	stat, e := os.Stat(path)
	if e != nil || stat.Size() <= 0 || stat.Size() >= maxBytes {
		os.Remove(path)
		return "", "", 0, 0, 0, errors.New("prepared media exceeds limit")
	}
	output, e = p.command(ctx, probe, "-v", "error", "-show_entries", "format=duration:stream=codec_type,width,height", "-of", "json", path).Output()
	if e != nil || json.Unmarshal(output, &meta) != nil {
		os.Remove(path)
		return "", "", 0, 0, 0, errors.New("prepared media duration unavailable")
	}
	duration, _ = strconv.ParseFloat(meta.Format.Duration, 64)
	if duration <= 0 || duration > 3600 {
		os.Remove(path)
		return "", "", 0, 0, 0, errors.New("prepared media duration outside limits")
	}
	for _, stream := range meta.Streams {
		if stream.Type == "video" {
			width, height = stream.Width, stream.Height
		}
	}
	return path, mime, duration, width, height, nil
}
