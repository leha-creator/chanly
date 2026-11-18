package media

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestVoicePreparation(t *testing.T) {
	binary, e := exec.LookPath("ffmpeg")
	if e != nil {
		t.Skip("FFmpeg is not installed")
	}
	src := filepath.Join(t.TempDir(), "voice.m4a")
	e = exec.Command(binary, "-v", "error", "-f", "lavfi", "-i", "sine=frequency=400:duration=1", "-c:a", "aac", src).Run()
	if e != nil {
		t.Fatal("failed to create synthetic audio")
	}
	path, mime, d, _, _, e := (Processor{Binary: binary}).Prepare(context.Background(), "audio", src, 1<<20)
	if e != nil {
		t.Fatal(e)
	}
	defer os.Remove(path)
	if mime != "audio/mpeg" || d < 0.9 || d > 1.2 {
		t.Fatal("invalid voice output")
	}
}

func TestMediaRecorderWebMWithoutDuration(t *testing.T) {
	binary, e := exec.LookPath("ffmpeg")
	if e != nil {
		t.Skip("FFmpeg is not installed")
	}
	path, mime, d, _, _, e := (Processor{Binary: binary}).Prepare(context.Background(), "audio", filepath.Join("testdata", "browser-voice.webm"), 1<<20)
	if e != nil {
		t.Fatal(e)
	}
	defer os.Remove(path)
	if mime != "audio/mpeg" || d < 1 || d > 3 {
		t.Fatalf("invalid browser voice: MIME=%s duration=%f", mime, d)
	}
}

func TestVideoPreparationWithOddDimensions(t *testing.T) {
	binary, e := exec.LookPath("ffmpeg")
	if e != nil {
		t.Skip("FFmpeg is not installed")
	}
	src := filepath.Join(t.TempDir(), "video.mkv")
	e = exec.Command(binary, "-v", "error", "-f", "lavfi", "-i", "testsrc=size=321x241:rate=5:duration=1", "-f", "lavfi", "-i", "sine=frequency=400:duration=1", "-c:v", "ffv1", "-c:a", "pcm_s16le", src).Run()
	if e != nil {
		t.Fatal("failed to create synthetic video")
	}
	path, mime, duration, width, height, e := (Processor{Binary: binary}).Prepare(context.Background(), "video", src, 2<<20)
	if e != nil {
		t.Fatal(e)
	}
	defer os.Remove(path)
	if mime != "video/mp4" || duration < 0.9 || duration > 1.2 || width != 320 || height%2 != 0 {
		t.Fatal("invalid prepared video")
	}
}
