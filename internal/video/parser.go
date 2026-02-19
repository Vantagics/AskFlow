// Package video provides video parsing functionality including RapidSpeech transcript
// parsing, keyframe management, and serialization utilities.
package video

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"askflow/internal/config"
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

// isRapidSpeechLogLine 判断一行是否为 RapidSpeech/SenseVoice 的日志输出。
// rs-asr-offline 会将模型加载、GPU 初始化、性能统计等日志写入 stdout，
// 这些日志行不是转录文本，需要过滤掉。
func isRapidSpeechLogLine(line string) bool {
	// 常见的日志行特征关键词
	logPatterns := []string{
		"processing time",
		"encoder processing",
		"decoder processing",
		"model path",
		"model loaded",
		"thread",
		"CMVN",
		"RTF",
		"Real-Time Factor",
		"GPU",
		"CPU",
		"gguf",
		"ggml",
		"sense-voice",
		"SenseVoice",
		"RapidSpeech",
		"rs-asr",
		"loading model",
		"初始化",
		"加载模型",
		"线程",
		"回退",
		"fallback",
	}
	lower := strings.ToLower(line)
	for _, p := range logPatterns {
		if strings.Contains(lower, strings.ToLower(p)) {
			return true
		}
	}
	// 以数字+点开头且包含冒号的行通常是日志（如 "1. Encoder processing time: 0.648946"）
	trimmed := strings.TrimSpace(line)
	if len(trimmed) > 2 && trimmed[0] >= '0' && trimmed[0] <= '9' && trimmed[1] == '.' && strings.Contains(trimmed, ":") {
		// 检查冒号后面是否跟着数字（日志格式），而非普通文本
		idx := strings.Index(trimmed, ":")
		if idx > 0 && idx < len(trimmed)-1 {
			after := strings.TrimSpace(trimmed[idx+1:])
			if len(after) > 0 && (after[0] >= '0' && after[0] <= '9' || after[0] == '-' || after[0] == '"') {
				return true
			}
		}
	}
	return false
}

// filterRapidSpeechOutput 从 RapidSpeech 的 stdout 输出中过滤掉日志行，
// 只保留实际的语音转录文本。
func filterRapidSpeechOutput(raw string) string {
	lines := strings.Split(raw, "\n")
	var transcriptLines []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if isRapidSpeechLogLine(trimmed) {
			continue
		}
		transcriptLines = append(transcriptLines, trimmed)
	}
	return strings.Join(transcriptLines, "\n")
}

// ParseRapidSpeechOutput 解析 RapidSpeech 文本输出为 TranscriptSegment 列表
// RapidSpeech输出格式为纯文本，我们将整段文本作为一个segment
// 注意：会自动过滤掉 rs-asr-offline 混入 stdout 的日志行
func ParseRapidSpeechOutput(textData string) []TranscriptSegment {
	text := filterRapidSpeechOutput(textData)
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

// DepsCheckResult 依赖检测结果，包含详细错误信息
type DepsCheckResult struct {
	FFmpegOK       bool   `json:"ffmpeg_ok"`
	FFmpegError    string `json:"ffmpeg_error,omitempty"`
	RapidSpeechOK  bool   `json:"rapidspeech_ok"`
	RapidSpeechError string `json:"rapidspeech_error,omitempty"`
}

// CheckDependencies 检测 ffmpeg 和 RapidSpeech 是否可用，返回详细结果
func (p *Parser) CheckDependencies() *DepsCheckResult {
	result := &DepsCheckResult{}

	// 检测 ffmpeg
	if p.FFmpegPath == "" {
		result.FFmpegError = "FFmpeg 路径未配置"
	} else {
		if info, err := os.Stat(p.FFmpegPath); err != nil {
			result.FFmpegError = fmt.Sprintf("FFmpeg 文件不存在: %s", p.FFmpegPath)
		} else if info.IsDir() {
			result.FFmpegError = fmt.Sprintf("FFmpeg 路径指向目录而非文件: %s", p.FFmpegPath)
		} else {
			cmd := exec.Command(p.FFmpegPath, "-version")
			if output, err := cmd.CombinedOutput(); err != nil {
				result.FFmpegError = fmt.Sprintf("FFmpeg 执行失败: %s", strings.TrimSpace(string(output)))
				if result.FFmpegError == "FFmpeg 执行失败: " {
					result.FFmpegError = fmt.Sprintf("FFmpeg 执行失败: %v", err)
				}
			} else {
				result.FFmpegOK = true
			}
		}
	}

	// 检测 RapidSpeech
	if p.RapidSpeechPath == "" && p.RapidSpeechModel == "" {
		result.RapidSpeechError = "RapidSpeech 可执行文件和模型路径均未配置"
	} else if p.RapidSpeechPath == "" {
		result.RapidSpeechError = "RapidSpeech 可执行文件路径未配置"
	} else if p.RapidSpeechModel == "" {
		result.RapidSpeechError = "RapidSpeech 模型文件路径未配置"
	} else {
		// 检查可执行文件
		info, err := os.Stat(p.RapidSpeechPath)
		if err != nil {
			result.RapidSpeechError = fmt.Sprintf("RapidSpeech 可执行文件不存在: %s", p.RapidSpeechPath)
		} else if info.IsDir() {
			result.RapidSpeechError = fmt.Sprintf("RapidSpeech 路径指向目录而非文件: %s", p.RapidSpeechPath)
		} else if runtime.GOOS != "windows" && info.Mode()&0111 == 0 {
			result.RapidSpeechError = fmt.Sprintf("RapidSpeech 可执行文件没有执行权限: %s", p.RapidSpeechPath)
		} else {
			// 检查模型文件
			mInfo, mErr := os.Stat(p.RapidSpeechModel)
			if mErr != nil {
				result.RapidSpeechError = fmt.Sprintf("RapidSpeech 模型文件不存在: %s", p.RapidSpeechModel)
			} else if mInfo.IsDir() {
				result.RapidSpeechError = fmt.Sprintf("RapidSpeech 模型路径指向目录而非文件: %s", p.RapidSpeechModel)
			} else {
				result.RapidSpeechOK = true
			}
		}
	}

	return result
}

// ValidateRapidSpeechConfig 详细验证 RapidSpeech 配置，返回具体错误信息
func (p *Parser) ValidateRapidSpeechConfig() (errors []string) {
	if p.RapidSpeechPath == "" && p.RapidSpeechModel == "" {
		return nil // 未配置，不需要验证
	}

	// 验证可执行文件
	if p.RapidSpeechPath == "" {
		errors = append(errors, "RapidSpeech 可执行文件路径未填写")
	} else {
		info, err := os.Stat(p.RapidSpeechPath)
		if err != nil {
			errors = append(errors, fmt.Sprintf("RapidSpeech 可执行文件不存在: %s", p.RapidSpeechPath))
		} else if info.IsDir() {
			errors = append(errors, fmt.Sprintf("RapidSpeech 路径指向目录而非文件: %s", p.RapidSpeechPath))
		} else if runtime.GOOS != "windows" && info.Mode()&0111 == 0 {
			// 尝试设置执行权限
			if chmodErr := os.Chmod(p.RapidSpeechPath, info.Mode()|0755); chmodErr != nil {
				errors = append(errors, fmt.Sprintf("RapidSpeech 可执行文件没有执行权限且无法自动设置: %v", chmodErr))
			}
		}
	}

	// 验证模型文件
	if p.RapidSpeechModel == "" {
		errors = append(errors, "RapidSpeech 模型文件路径未填写")
	} else {
		info, err := os.Stat(p.RapidSpeechModel)
		if err != nil {
			errors = append(errors, fmt.Sprintf("RapidSpeech 模型文件不存在: %s", p.RapidSpeechModel))
		} else if info.IsDir() {
			errors = append(errors, fmt.Sprintf("RapidSpeech 模型路径指向目录而非文件: %s", p.RapidSpeechModel))
		} else {
			lower := strings.ToLower(p.RapidSpeechModel)
			if !strings.HasSuffix(lower, ".gguf") && !strings.HasSuffix(lower, ".bin") {
				errors = append(errors, "RapidSpeech 模型文件应为 .gguf 或 .bin 格式")
			}
		}
	}

	return errors
}

// ExtractAudio 调用 ffmpeg 将视频的音频轨提取为 16kHz 单声道 WAV 文件
func (p *Parser) ExtractAudio(videoPath, outputPath string) error {
	if p.FFmpegPath == "" {
		return fmt.Errorf("ffmpeg 路径未配置")
	}
	// Validate paths don't contain shell metacharacters
	for _, path := range []string{videoPath, outputPath} {
		if strings.ContainsAny(path, "|;&$`") {
			return fmt.Errorf("路径包含非法字符: %s", path)
		}
	}
	cmd := exec.Command(p.FFmpegPath,
		"-i", videoPath,
		"-vn",
		"-acodec", "pcm_s16le",
		"-ar", "16000",
		"-ac", "1",
		"-y",
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
	// Validate paths don't contain shell metacharacters
	if strings.ContainsAny(audioPath, "|;&$`") {
		return nil, fmt.Errorf("音频路径包含非法字符")
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

	// 解析输出文本，过滤掉 rs-asr-offline 混入 stdout 的日志行
	text := filterRapidSpeechOutput(string(output))
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
	// Validate paths don't contain shell metacharacters
	for _, path := range []string{videoPath, outputDir} {
		if strings.ContainsAny(path, "|;&$`") {
			return nil, fmt.Errorf("路径包含非法字符: %s", path)
		}
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

// ProbeDuration 调用 ffmpeg 获取视频时长（秒）。
// 通过 -show_entries format=duration 解析 stderr 中的 Duration 行。
func (p *Parser) ProbeDuration(videoPath string) float64 {
	if p.FFmpegPath == "" {
		return 0
	}
	// 使用 ffmpeg -i 读取时长，ffmpeg 会在 stderr 输出 Duration: HH:MM:SS.xx
	cmd := exec.Command(p.FFmpegPath, "-i", videoPath, "-f", "null", "-")
	output, _ := cmd.CombinedOutput()
	// 解析 "Duration: 00:12:34.56" 格式
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		idx := strings.Index(line, "Duration:")
		if idx < 0 {
			continue
		}
		durStr := strings.TrimSpace(line[idx+len("Duration:"):])
		if commaIdx := strings.Index(durStr, ","); commaIdx > 0 {
			durStr = durStr[:commaIdx]
		}
		durStr = strings.TrimSpace(durStr)
		if durStr == "N/A" {
			return 0
		}
		// 解析 HH:MM:SS.xx
		parts := strings.Split(durStr, ":")
		if len(parts) == 3 {
			var h, m, s float64
			fmt.Sscanf(parts[0], "%f", &h)
			fmt.Sscanf(parts[1], "%f", &m)
			fmt.Sscanf(parts[2], "%f", &s)
			return h*3600 + m*60 + s
		}
	}
	return 0
}

// Parse 编排完整的视频解析流程：提取音频转录 + 抽取关键帧
func (p *Parser) Parse(videoPath string) (*ParseResult, error) {
	tempDir, err := os.MkdirTemp("", "video-parse-*")
	if err != nil {
		return nil, fmt.Errorf("创建临时目录失败: %w", err)
	}
	defer os.RemoveAll(tempDir)

	result := &ParseResult{}

	// 探测视频时长
	result.Duration = p.ProbeDuration(videoPath)

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

