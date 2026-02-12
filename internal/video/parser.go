// Package video provides video parsing functionality including RapidSpeech transcript
// parsing, keyframe management, and serialization utilities.
package video

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"helpdesk/internal/config"
)

// TranscriptSegment 表示语音识别输出的一个转录片段
type TranscriptSegment struct {
	Start float64 `json:"start"` // 起始时间（秒）
	End   float64 `json:"end"`   // 结束时间（秒）
	Text  string  `json:"text"`  // 转录文本
}

// Keyframe 表示从视频中提取的一个关键帧
type Keyframe struct {
	Timestamp float64 // 帧在视频中的时间（秒）
	FilePath  string  // 帧图像文件的临时路径
	Data      []byte  // 帧图像数据（Parse 返回前读入内存）
}

// ParseResult 视频解析结果
type ParseResult struct {
	Transcript []TranscriptSegment // 转录片段列表（可能为空）
	Keyframes  []Keyframe          // 关键帧列表
	Duration   float64             // 视频总时长（秒）
}

// ParseRapidSpeechOutput 解析 RapidSpeech 文本输出为 TranscriptSegment 列表
// RapidSpeech输出格式为纯文本，我们将整段文本作为一个segment
func ParseRapidSpeechOutput(textData string) []TranscriptSegment {
	text := strings.TrimSpace(textData)
	if text == "" {
		return []TranscriptSegment{}
	}
	// 将整个转录文本作为一个segment，时间范围为0到end
	return []TranscriptSegment{
		{
			Start: 0,
			End:   0, // 实际的end时间会在后续填充
			Text:  text,
		},
	}
}

// SerializeTranscript 将 TranscriptSegment 列表序列化为 JSON
func SerializeTranscript(segments []TranscriptSegment) ([]byte, error) {
	return json.Marshal(segments)
}

// Parser 视频解析器，封装 ffmpeg 和 RapidSpeech 的调用逻辑
type Parser struct {
	FFmpegPath        string
	RapidSpeechPath   string
	KeyframeInterval  int
	RapidSpeechModel  string
}

// NewParser 根据 VideoConfig 创建 Parser 实例
func NewParser(cfg config.VideoConfig) *Parser {
	interval := cfg.KeyframeInterval
	if interval <= 0 {
		interval = 10
	}
	return &Parser{
		FFmpegPath:       cfg.FFmpegPath,
		RapidSpeechPath:  cfg.RapidSpeechPath,
		KeyframeInterval: interval,
		RapidSpeechModel: cfg.RapidSpeechModel,
	}
}

// CheckDependencies 检测 ffmpeg 和 RapidSpeech 是否可用
func (p *Parser) CheckDependencies() (ffmpegOK bool, rapidSpeechOK bool) {
	if p.FFmpegPath != "" {
		cmd := exec.Command(p.FFmpegPath, "-version")
		if err := cmd.Run(); err == nil {
			ffmpegOK = true
		}
	}
	if p.RapidSpeechPath != "" && p.RapidSpeechModel != "" {
		// 检查 rs-asr-offline 可执行文件是否存在
		if _, err := os.Stat(p.RapidSpeechPath); err == nil {
			// 检查模型文件是否存在
			if _, err := os.Stat(p.RapidSpeechModel); err == nil {
				rapidSpeechOK = true
			}
		}
	}
	return
}

// ExtractAudio 调用 ffmpeg 将视频的音频轨提取为 16kHz 单声道 WAV 文件
func (p *Parser) ExtractAudio(videoPath, outputPath string) error {
	if p.FFmpegPath == "" {
		return fmt.Errorf("ffmpeg 路径未配置")
	}
	cmd := exec.Command(p.FFmpegPath,
		"-i", videoPath,
		"-vn",
		"-acodec", "pcm_s16le",
		"-ar", "16000",
		"-ac", "1",
		outputPath,
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ffmpeg 音频提取失败: %s: %w", strings.TrimSpace(string(output)), err)
	}
	return nil
}

// Transcribe 调用 RapidSpeech CLI 对音频进行语音转录
func (p *Parser) Transcribe(audioPath string) ([]TranscriptSegment, error) {
	if p.RapidSpeechPath == "" {
		return nil, fmt.Errorf("RapidSpeech 路径未配置")
	}
	if p.RapidSpeechModel == "" {
		return nil, fmt.Errorf("RapidSpeech 模型路径未配置")
	}

	// RapidSpeech.cpp 命令行格式：
	// rs-asr-offline -m model.gguf -w audio.wav
	cmd := exec.Command(p.RapidSpeechPath,
		"-m", p.RapidSpeechModel,
		"-w", audioPath,
	)

	// 捕获标准输出
	output, err := cmd.Output()
	if err != nil {
		stderr := ""
		if exitErr, ok := err.(*exec.ExitError); ok {
			stderr = string(exitErr.Stderr)
		}
		return nil, fmt.Errorf("RapidSpeech 转录失败: %s: %w", strings.TrimSpace(stderr), err)
	}

	// 解析输出文本
	text := strings.TrimSpace(string(output))
	if text == "" {
		return []TranscriptSegment{}, nil
	}

	// 返回单个segment，包含整个转录文本
	return []TranscriptSegment{
		{
			Start: 0,
			End:   0, // 可以从视频时长推算
			Text:  text,
		},
	}, nil
}

// ExtractKeyframes 调用 ffmpeg 按 KeyframeInterval 间隔从视频中提取关键帧图像
func (p *Parser) ExtractKeyframes(videoPath, outputDir string) ([]Keyframe, error) {
	if p.FFmpegPath == "" {
		return nil, fmt.Errorf("ffmpeg 路径未配置")
	}

	outputPattern := filepath.Join(outputDir, "frame_%04d.jpg")
	cmd := exec.Command(p.FFmpegPath,
		"-i", videoPath,
		"-vf", fmt.Sprintf("fps=1/%d", p.KeyframeInterval),
		"-q:v", "2",
		outputPattern,
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("ffmpeg 关键帧提取失败: %s: %w", strings.TrimSpace(string(output)), err)
	}

	// Scan output directory for generated frame files
	entries, err := os.ReadDir(outputDir)
	if err != nil {
		return nil, fmt.Errorf("读取关键帧目录失败: %w", err)
	}

	var frameFiles []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasPrefix(entry.Name(), "frame_") && strings.HasSuffix(entry.Name(), ".jpg") {
			frameFiles = append(frameFiles, entry.Name())
		}
	}
	sort.Strings(frameFiles)

	keyframes := make([]Keyframe, 0, len(frameFiles))
	for i, name := range frameFiles {
		keyframes = append(keyframes, Keyframe{
			Timestamp: float64(i * p.KeyframeInterval),
			FilePath:  filepath.Join(outputDir, name),
		})
	}

	return keyframes, nil
}

// Parse 编排完整的视频解析流程：提取音频转录 + 抽取关键帧
func (p *Parser) Parse(videoPath string) (*ParseResult, error) {
	tempDir, err := os.MkdirTemp("", "video-parse-*")
	if err != nil {
		return nil, fmt.Errorf("创建临时目录失败: %w", err)
	}
	defer os.RemoveAll(tempDir)

	result := &ParseResult{}

	// 音频转录（仅�� RapidSpeech 已配置时执行）
	if p.RapidSpeechPath != "" && p.RapidSpeechModel != "" {
		audioPath := filepath.Join(tempDir, "audio.wav")
		audioErr := p.ExtractAudio(videoPath, audioPath)
		if audioErr != nil {
			// 如果音频提取失败，可能是视频没有音频轨，跳过转录继续关键帧提取
			// 不返回错误，仅跳过转录步骤
		} else {
			segments, transcribeErr := p.Transcribe(audioPath)
			if transcribeErr != nil {
				return nil, transcribeErr
			}
			result.Transcript = segments
		}
	}

	// 关键帧提取（仅当 ffmpeg 已配置时执行）
	if p.FFmpegPath != "" {
		framesDir := filepath.Join(tempDir, "frames")
		if mkErr := os.MkdirAll(framesDir, 0o755); mkErr != nil {
			return nil, fmt.Errorf("创建关键帧目录失败: %w", mkErr)
		}
		keyframes, kfErr := p.ExtractKeyframes(videoPath, framesDir)
		if kfErr != nil {
			return nil, kfErr
		}
		// Read keyframe image data into memory before tempDir is cleaned up by defer.
		for i := range keyframes {
			data, err := os.ReadFile(keyframes[i].FilePath)
			if err != nil {
				return nil, fmt.Errorf("读取关键帧 %d 失败: %w", i, err)
			}
			keyframes[i].Data = data
		}
		result.Keyframes = keyframes
	}

	return result, nil
}

