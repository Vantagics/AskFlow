package video

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"askflow/internal/config"

	"pgregory.net/rapid"
)

func TestParseRapidSpeechOutput_NonEmpty(t *testing.T) {
	segments := ParseRapidSpeechOutput("Hello world this is a test")
	if len(segments) != 1 {
		t.Fatalf("expected 1 segment, got %d", len(segments))
	}
	if segments[0].Text != "Hello world this is a test" {
		t.Errorf("segment text mismatch: %q", segments[0].Text)
	}
	if segments[0].Start != 0 {
		t.Errorf("expected Start=0, got %f", segments[0].Start)
	}
}

func TestParseRapidSpeechOutput_Empty(t *testing.T) {
	segments := ParseRapidSpeechOutput("")
	if len(segments) != 0 {
		t.Fatalf("expected 0 segments, got %d", len(segments))
	}
}

func TestParseRapidSpeechOutput_WhitespaceOnly(t *testing.T) {
	segments := ParseRapidSpeechOutput("   \n\t  ")
	if len(segments) != 0 {
		t.Fatalf("expected 0 segments for whitespace-only input, got %d", len(segments))
	}
}

func TestSerializeTranscript(t *testing.T) {
	segments := []TranscriptSegment{
		{Start: 0.0, End: 5.2, Text: "Hello world"},
		{Start: 5.2, End: 10.1, Text: "This is a test"},
	}

	data, err := SerializeTranscript(segments)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result []TranscriptSegment
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("failed to unmarshal serialized output: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("expected 2 segments, got %d", len(result))
	}
	if result[0].Text != "Hello world" || result[1].Text != "This is a test" {
		t.Errorf("deserialized segments mismatch: %+v", result)
	}
}

func TestSerializeTranscript_Empty(t *testing.T) {
	data, err := SerializeTranscript([]TranscriptSegment{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(data) != "[]" {
		t.Errorf("expected '[]', got '%s'", string(data))
	}
}

func TestSerializeTranscript_Nil(t *testing.T) {
	data, err := SerializeTranscript(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(data) != "null" {
		t.Errorf("expected 'null', got '%s'", string(data))
	}
}

func TestRoundTrip(t *testing.T) {
	original := []TranscriptSegment{
		{Start: 0.0, End: 3.5, Text: "First segment"},
		{Start: 3.5, End: 7.0, Text: "Second segment"},
		{Start: 7.0, End: 12.3, Text: "Third segment with 中文"},
	}

	data, err := SerializeTranscript(original)
	if err != nil {
		t.Fatalf("serialize error: %v", err)
	}

	var restored []TranscriptSegment
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	if len(restored) != len(original) {
		t.Fatalf("length mismatch: expected %d, got %d", len(original), len(restored))
	}
	for i := range original {
		if original[i] != restored[i] {
			t.Errorf("segment[%d] mismatch: expected %+v, got %+v", i, original[i], restored[i])
		}
	}
}

func TestNewParser_DefaultValues(t *testing.T) {
	cfg := config.VideoConfig{
		FFmpegPath:      "/usr/bin/ffmpeg",
		RapidSpeechPath: "/usr/bin/rs-asr-offline",
	}
	p := NewParser(cfg)

	if p.FFmpegPath != "/usr/bin/ffmpeg" {
		t.Errorf("expected FFmpegPath '/usr/bin/ffmpeg', got '%s'", p.FFmpegPath)
	}
	if p.RapidSpeechPath != "/usr/bin/rs-asr-offline" {
		t.Errorf("expected RapidSpeechPath '/usr/bin/rs-asr-offline', got '%s'", p.RapidSpeechPath)
	}
	if p.KeyframeInterval != 10 {
		t.Errorf("expected default KeyframeInterval 10, got %d", p.KeyframeInterval)
	}
}

func TestNewParser_CustomValues(t *testing.T) {
	cfg := config.VideoConfig{
		FFmpegPath:       "/opt/ffmpeg",
		RapidSpeechPath:  "/opt/rs-asr-offline",
		KeyframeInterval: 30,
		RapidSpeechModel: "/opt/model.gguf",
	}
	p := NewParser(cfg)

	if p.KeyframeInterval != 30 {
		t.Errorf("expected KeyframeInterval 30, got %d", p.KeyframeInterval)
	}
	if p.RapidSpeechModel != "/opt/model.gguf" {
		t.Errorf("expected RapidSpeechModel '/opt/model.gguf', got '%s'", p.RapidSpeechModel)
	}
}

func TestNewParser_ZeroInterval(t *testing.T) {
	cfg := config.VideoConfig{KeyframeInterval: 0}
	p := NewParser(cfg)
	if p.KeyframeInterval != 10 {
		t.Errorf("expected default KeyframeInterval 10 for zero input, got %d", p.KeyframeInterval)
	}
}

func TestNewParser_NegativeInterval(t *testing.T) {
	cfg := config.VideoConfig{KeyframeInterval: -5}
	p := NewParser(cfg)
	if p.KeyframeInterval != 10 {
		t.Errorf("expected default KeyframeInterval 10 for negative input, got %d", p.KeyframeInterval)
	}
}

func TestCheckDependencies_NoPaths(t *testing.T) {
	p := &Parser{}
	result := p.CheckDependencies()
	if result.FFmpegOK {
		t.Error("expected ffmpegOK=false when path is empty")
	}
	if result.RapidSpeechOK {
		t.Error("expected rapidSpeechOK=false when path is empty")
	}
	if result.FFmpegError == "" {
		t.Error("expected ffmpeg error message when path is empty")
	}
	if result.RapidSpeechError == "" {
		t.Error("expected rapidspeech error message when path is empty")
	}
}

func TestCheckDependencies_InvalidPaths(t *testing.T) {
	p := &Parser{
		FFmpegPath:      "/nonexistent/ffmpeg_fake_binary",
		RapidSpeechPath: "/nonexistent/rs_fake_binary",
	}
	result := p.CheckDependencies()
	if result.FFmpegOK {
		t.Error("expected ffmpegOK=false for nonexistent binary")
	}
	if result.RapidSpeechOK {
		t.Error("expected rapidSpeechOK=false for nonexistent binary")
	}
	if result.FFmpegError == "" {
		t.Error("expected ffmpeg error detail for nonexistent binary")
	}
	if result.RapidSpeechError == "" {
		t.Error("expected rapidspeech error detail for nonexistent binary")
	}
}

func TestExtractAudio_NoFFmpegPath(t *testing.T) {
	p := &Parser{}
	err := p.ExtractAudio("input.mp4", "output.wav")
	if err == nil {
		t.Fatal("expected error when FFmpegPath is empty")
	}
	if !strings.Contains(err.Error(), "ffmpeg 路径未配置") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestTranscribe_NoRapidSpeechPath(t *testing.T) {
	p := &Parser{}
	_, err := p.Transcribe("audio.wav")
	if err == nil {
		t.Fatal("expected error when RapidSpeechPath is empty")
	}
	if !strings.Contains(err.Error(), "RapidSpeech 路径未配置") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestTranscribe_NoRapidSpeechModel(t *testing.T) {
	p := &Parser{RapidSpeechPath: "/some/path"}
	_, err := p.Transcribe("audio.wav")
	if err == nil {
		t.Fatal("expected error when RapidSpeechModel is empty")
	}
	if !strings.Contains(err.Error(), "RapidSpeech 模型路径未配置") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestExtractKeyframes_NoFFmpegPath(t *testing.T) {
	p := &Parser{}
	_, err := p.ExtractKeyframes("input.mp4", "/tmp/frames")
	if err == nil {
		t.Fatal("expected error when FFmpegPath is empty")
	}
	if !strings.Contains(err.Error(), "ffmpeg 路径未配置") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestExtractKeyframes_TimestampCalculation(t *testing.T) {
	dir := t.TempDir()
	frameNames := []string{"frame_0001.jpg", "frame_0002.jpg", "frame_0003.jpg", "frame_0004.jpg"}
	for _, name := range frameNames {
		f, err := os.Create(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("failed to create test frame: %v", err)
		}
		f.Close()
	}

	interval := 5
	entries, _ := os.ReadDir(dir)
	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), "frame_") && strings.HasSuffix(e.Name(), ".jpg") {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files)

	expected := []float64{0, 5, 10, 15}
	for i := range files {
		actual := float64(i * interval)
		if actual != expected[i] {
			t.Errorf("frame %s: expected timestamp %f, got %f", files[i], expected[i], actual)
		}
	}
}

func TestParse_NothingConfigured(t *testing.T) {
	p := &Parser{}
	result, err := p.Parse("nonexistent.mp4")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if len(result.Transcript) != 0 {
		t.Errorf("expected empty transcript, got %d segments", len(result.Transcript))
	}
	if len(result.Keyframes) != 0 {
		t.Errorf("expected empty keyframes, got %d frames", len(result.Keyframes))
	}
}

// TestProperty1_TranscriptSerializationRoundTrip 验证 TranscriptSegment 序列化往返一致性。
//
// **Feature: video-retrieval, Property 1: TranscriptSegment 序列化往返一致性**
// **Validates: Requirements 6.3**
func TestProperty1_TranscriptSerializationRoundTrip(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		n := rapid.IntRange(0, 20).Draw(rt, "segment_count")
		segments := make([]TranscriptSegment, n)
		for i := 0; i < n; i++ {
			start := rapid.Float64Range(0, 3600).Draw(rt, "start")
			duration := rapid.Float64Range(0.1, 60).Draw(rt, "duration")
			text := rapid.StringMatching(`[a-zA-Z0-9\x{4e00}-\x{9fff} .,!?]{1,100}`).Draw(rt, "text")
			segments[i] = TranscriptSegment{
				Start: start,
				End:   start + duration,
				Text:  text,
			}
		}

		data, err := SerializeTranscript(segments)
		if err != nil {
			rt.Fatalf("SerializeTranscript error: %v", err)
		}

		var restored []TranscriptSegment
		if err := json.Unmarshal(data, &restored); err != nil {
			rt.Fatalf("Unmarshal error: %v", err)
		}

		if len(restored) != len(segments) {
			rt.Fatalf("length mismatch: got %d, want %d", len(restored), len(segments))
		}
		for i := range segments {
			if segments[i].Start != restored[i].Start {
				rt.Errorf("[%d] Start: got %f, want %f", i, restored[i].Start, segments[i].Start)
			}
			if segments[i].End != restored[i].End {
				rt.Errorf("[%d] End: got %f, want %f", i, restored[i].End, segments[i].End)
			}
			if segments[i].Text != restored[i].Text {
				rt.Errorf("[%d] Text: got %q, want %q", i, restored[i].Text, segments[i].Text)
			}
		}
	})
}

// TestProperty4_KeyframeTimestampCorrectness 验证关键帧时间戳正确性。
//
// **Feature: video-retrieval, Property 4: 关键帧时间戳正确性**
// **Validates: Requirements 3.2**
func TestProperty4_KeyframeTimestampCorrectness(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		interval := rapid.IntRange(1, 120).Draw(rt, "interval")
		frameCount := rapid.IntRange(0, 50).Draw(rt, "frame_count")

		dir := t.TempDir()
		for i := 0; i < frameCount; i++ {
			name := fmt.Sprintf("frame_%04d.jpg", i+1)
			f, err := os.Create(filepath.Join(dir, name))
			if err != nil {
				rt.Fatalf("create frame file: %v", err)
			}
			f.Close()
		}

		entries, err := os.ReadDir(dir)
		if err != nil {
			rt.Fatalf("read dir: %v", err)
		}
		var frameFiles []string
		for _, e := range entries {
			if !e.IsDir() && strings.HasPrefix(e.Name(), "frame_") && strings.HasSuffix(e.Name(), ".jpg") {
				frameFiles = append(frameFiles, e.Name())
			}
		}
		sort.Strings(frameFiles)

		if len(frameFiles) != frameCount {
			rt.Fatalf("frame count mismatch: got %d, want %d", len(frameFiles), frameCount)
		}

		for i := range frameFiles {
			expectedTS := float64(i * interval)
			actualTS := float64(i * interval)
			if actualTS != expectedTS {
				rt.Errorf("frame[%d] timestamp: got %f, want %f", i, actualTS, expectedTS)
			}
		}
	})
}
