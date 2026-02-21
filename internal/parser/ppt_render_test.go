package parser

import (
	"bytes"
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func TestPPTRenderImages(t *testing.T) {
	pptxPath := filepath.Join("..", "..", "test.pptx")
	data, err := os.ReadFile(pptxPath)
	if err != nil {
		t.Fatalf("无法读取test.pptx: %v", err)
	}

	dp := &DocumentParser{}
	result, err := dp.parsePPT(data)
	if err != nil {
		t.Fatalf("解析PPT失败: %v", err)
	}

	t.Logf("幻灯片数量: %s", result.Metadata["slide_count"])
	t.Logf("渲染图片数量: %s", result.Metadata["image_count"])
	t.Logf("提取文本长度: %d 字符", len(result.Text))

	if len(result.Images) == 0 {
		t.Fatal("未渲染出任何图片")
	}

	outDir := filepath.Join(os.TempDir(), "goppt_render_test")
	os.MkdirAll(outDir, 0755)
	t.Logf("渲染图片输出目录: %s", outDir)

	for i, img := range result.Images {
		_, decErr := png.DecodeConfig(bytes.NewReader(img.Data))
		if decErr != nil {
			t.Errorf("Slide %d: PNG数据无效: %v", i+1, decErr)
		}

		outPath := filepath.Join(outDir, fmt.Sprintf("slide_%d.png", i+1))
		if err := os.WriteFile(outPath, img.Data, 0644); err != nil {
			t.Errorf("写入第%d页图片失败: %v", i+1, err)
			continue
		}

		fi, _ := os.Stat(outPath)
		t.Logf("Slide %d: size=%d bytes, alt=%q", i+1, fi.Size(), img.Alt)

		if len(img.Data) < 1000 {
			t.Errorf("Slide %d: 图片数据过小(%d bytes)，渲染可能不完整", i+1, len(img.Data))
		}
	}

	t.Logf("所有 %d 张幻灯片图片已保存到: %s", len(result.Images), outDir)
}
