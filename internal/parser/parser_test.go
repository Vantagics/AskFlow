package parser

import (
	"strings"
	"testing"
)

// --- File type validation tests ---

func TestParse_SupportedTypes(t *testing.T) {
	dp := &DocumentParser{}
	// We can't easily create valid file bytes for each format in a unit test,
	// but we can verify that supported types are dispatched (they'll fail on invalid data,
	// not on "unsupported format").
	supportedTypes := []string{"pdf", "word", "excel", "ppt", "html"}
	for _, ft := range supportedTypes {
		_, err := dp.Parse([]byte("invalid"), ft)
		// HTML parser can succeed on plain text (it's valid HTML), so skip nil-error check for html
		if err == nil {
			if ft != "html" {
				t.Errorf("expected error for invalid %s data, got nil", ft)
			}
			continue
		}
		if strings.Contains(err.Error(), "不支持的文件格式") {
			t.Errorf("type %q should be supported but got unsupported error: %v", ft, err)
		}
	}
}

func TestParse_SupportedTypesCaseInsensitive(t *testing.T) {
	dp := &DocumentParser{}
	variants := []string{"PDF", "Pdf", "WORD", "Word", "EXCEL", "Excel", "PPT", "Ppt", "HTML", "Html"}
	for _, ft := range variants {
		_, err := dp.Parse([]byte("invalid"), ft)
		if err == nil {
			continue // unlikely but fine
		}
		if strings.Contains(err.Error(), "不支持的文件格式") {
			t.Errorf("type %q should be supported (case-insensitive) but got unsupported error", ft)
		}
	}
}

func TestParse_UnsupportedTypes(t *testing.T) {
	dp := &DocumentParser{}
	unsupported := []string{"txt", "csv", "jpg", "png", "mp3", "", "unknown"}
	for _, ft := range unsupported {
		_, err := dp.Parse([]byte("data"), ft)
		if err == nil {
			t.Errorf("expected error for unsupported type %q, got nil", ft)
			continue
		}
		if !strings.Contains(err.Error(), "不支持的文件格式") {
			t.Errorf("expected '不支持的文件格式' error for type %q, got: %v", ft, err)
		}
	}
}

// --- CleanText tests ---

func TestCleanText_RemovesExcessiveSpaces(t *testing.T) {
	input := "hello    world"
	got := CleanText(input)
	if got != "hello world" {
		t.Errorf("expected 'hello world', got %q", got)
	}
}

func TestCleanText_RemovesTabs(t *testing.T) {
	input := "hello\t\tworld"
	got := CleanText(input)
	if got != "hello world" {
		t.Errorf("expected 'hello world', got %q", got)
	}
}

func TestCleanText_TrimsLeadingTrailingWhitespace(t *testing.T) {
	input := "  hello world  "
	got := CleanText(input)
	if got != "hello world" {
		t.Errorf("expected 'hello world', got %q", got)
	}
}

func TestCleanText_CollapsesNewlines(t *testing.T) {
	input := "hello\n\n\n\nworld"
	got := CleanText(input)
	if got != "hello\n\nworld" {
		t.Errorf("expected 'hello\\n\\nworld', got %q", got)
	}
}

func TestCleanText_RemovesControlCharacters(t *testing.T) {
	input := "hello\x00\x01\x02world"
	got := CleanText(input)
	if got != "helloworld" {
		t.Errorf("expected 'helloworld', got %q", got)
	}
}

func TestCleanText_PreservesNewlines(t *testing.T) {
	input := "line1\nline2"
	got := CleanText(input)
	if got != "line1\nline2" {
		t.Errorf("expected 'line1\\nline2', got %q", got)
	}
}

func TestCleanText_EmptyString(t *testing.T) {
	got := CleanText("")
	if got != "" {
		t.Errorf("expected empty string, got %q", got)
	}
}

func TestCleanText_OnlyWhitespace(t *testing.T) {
	got := CleanText("   \t\t  \n\n  ")
	if got != "" {
		t.Errorf("expected empty string, got %q", got)
	}
}

func TestCleanText_MixedWhitespaceAndControl(t *testing.T) {
	input := "  hello \x00  \t world \x7F  "
	got := CleanText(input)
	if got != "hello world" {
		t.Errorf("expected 'hello world', got %q", got)
	}
}

// --- Error handling tests ---

func TestParsePDF_InvalidData(t *testing.T) {
	dp := &DocumentParser{}
	_, err := dp.parsePDF([]byte("not a pdf"))
	if err == nil {
		t.Error("expected error for invalid PDF data")
		return
	}
	if !strings.Contains(err.Error(), "pdf解析错误") {
		t.Errorf("expected error containing 'pdf解析错误', got: %v", err)
	}
}

func TestParseWord_InvalidData(t *testing.T) {
	dp := &DocumentParser{}
	_, err := dp.parseWord([]byte("not a word doc"))
	if err == nil {
		t.Error("expected error for invalid Word data")
		return
	}
	if !strings.Contains(err.Error(), "word解析错误") {
		t.Errorf("expected error containing 'word解析错误', got: %v", err)
	}
}

func TestParseExcel_InvalidData(t *testing.T) {
	dp := &DocumentParser{}
	_, err := dp.parseExcel([]byte("not an excel file"))
	if err == nil {
		t.Error("expected error for invalid Excel data")
		return
	}
	if !strings.Contains(err.Error(), "excel解析错误") {
		t.Errorf("expected error containing 'excel解析错误', got: %v", err)
	}
}

func TestParsePPT_InvalidData(t *testing.T) {
	dp := &DocumentParser{}
	_, err := dp.parsePPT([]byte("not a ppt file"))
	if err == nil {
		t.Error("expected error for invalid PPT data")
		return
	}
	if !strings.Contains(err.Error(), "ppt解析错误") {
		t.Errorf("expected error containing 'ppt解析错误', got: %v", err)
	}
}

func TestParse_ErrorContainsFileType(t *testing.T) {
	dp := &DocumentParser{}
	// Unsupported type error should contain the file type
	_, err := dp.Parse([]byte("data"), "xyz")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "xyz") {
		t.Errorf("error should contain file type 'xyz', got: %v", err)
	}
}

// --- HTML parser tests ---

func TestParseHTML_BasicText(t *testing.T) {
	dp := &DocumentParser{}
	html := `<html><body><h1>Hello</h1><p>World</p></body></html>`
	result, err := dp.Parse([]byte(html), "html")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result.Text, "Hello") || !strings.Contains(result.Text, "World") {
		t.Errorf("expected text to contain 'Hello' and 'World', got: %q", result.Text)
	}
}

func TestParseHTML_StripsScriptAndStyle(t *testing.T) {
	dp := &DocumentParser{}
	html := `<html><head><style>body{color:red}</style></head><body>
		<script>alert('xss')</script><p>Content</p></body></html>`
	result, err := dp.Parse([]byte(html), "html")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(result.Text, "alert") || strings.Contains(result.Text, "color:red") {
		t.Errorf("script/style content should be stripped, got: %q", result.Text)
	}
	if !strings.Contains(result.Text, "Content") {
		t.Errorf("expected 'Content' in text, got: %q", result.Text)
	}
}

func TestParseHTML_ExtractsImages(t *testing.T) {
	dp := &DocumentParser{}
	html := `<html><body>
		<img src="https://example.com/logo.png" alt="Logo">
		<img src="/images/photo.jpg" alt="Photo">
		<p>Text content</p>
	</body></html>`
	result, err := dp.Parse([]byte(html), "html")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Images) != 2 {
		t.Fatalf("expected 2 images, got %d", len(result.Images))
	}
	if result.Images[0].URL != "https://example.com/logo.png" {
		t.Errorf("expected absolute URL, got: %s", result.Images[0].URL)
	}
	if result.Images[0].Alt != "Logo" {
		t.Errorf("expected alt 'Logo', got: %s", result.Images[0].Alt)
	}
}

func TestParseHTML_ResolvesRelativeImageURLs(t *testing.T) {
	dp := &DocumentParser{}
	html := `<html><body>
		<img src="/images/photo.jpg" alt="Photo">
		<img src="assets/icon.png" alt="Icon">
		<img src="https://cdn.example.com/abs.png" alt="Absolute">
	</body></html>`
	result, err := dp.ParseWithBaseURL([]byte(html), "html", "https://example.com/docs/page.html")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Images) != 3 {
		t.Fatalf("expected 3 images, got %d", len(result.Images))
	}
	if result.Images[0].URL != "https://example.com/images/photo.jpg" {
		t.Errorf("expected resolved URL, got: %s", result.Images[0].URL)
	}
	if result.Images[1].URL != "https://example.com/docs/assets/icon.png" {
		t.Errorf("expected resolved relative URL, got: %s", result.Images[1].URL)
	}
	if result.Images[2].URL != "https://cdn.example.com/abs.png" {
		t.Errorf("absolute URL should remain unchanged, got: %s", result.Images[2].URL)
	}
}

func TestParseHTML_SkipsDataURIs(t *testing.T) {
	dp := &DocumentParser{}
	html := `<html><body>
		<img src="data:image/png;base64,iVBOR..." alt="Inline">
		<img src="https://example.com/real.png" alt="Real">
	</body></html>`
	result, err := dp.Parse([]byte(html), "html")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Images) != 1 {
		t.Fatalf("expected 1 image (data URI skipped), got %d", len(result.Images))
	}
	if result.Images[0].Alt != "Real" {
		t.Errorf("expected 'Real' image, got: %s", result.Images[0].Alt)
	}
}

func TestParseHTML_DecodesEntities(t *testing.T) {
	dp := &DocumentParser{}
	html := `<html><body><p>A &amp; B &lt; C &gt; D &quot;E&quot; &#39;F&#39;</p></body></html>`
	result, err := dp.Parse([]byte(html), "html")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result.Text, `A & B < C > D "E" 'F'`) {
		t.Errorf("HTML entities not decoded properly, got: %q", result.Text)
	}
}

func TestParseHTML_EmptyContent(t *testing.T) {
	dp := &DocumentParser{}
	_, err := dp.Parse([]byte(""), "html")
	if err == nil {
		t.Error("expected error for empty HTML")
	}
}

func TestParseHTML_OnlyWhitespace(t *testing.T) {
	dp := &DocumentParser{}
	_, err := dp.Parse([]byte("   \n\t  "), "html")
	if err == nil {
		t.Error("expected error for whitespace-only HTML")
	}
}

func TestParseHTML_BaseHrefTag(t *testing.T) {
	dp := &DocumentParser{}
	html := `<html><head><base href="https://example.com/docs/"></head><body>
		<img src="photo.jpg" alt="Photo">
	</body></html>`
	result, err := dp.Parse([]byte(html), "html")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Images) != 1 {
		t.Fatalf("expected 1 image, got %d", len(result.Images))
	}
	if result.Images[0].URL != "https://example.com/docs/photo.jpg" {
		t.Errorf("expected URL resolved via <base href>, got: %s", result.Images[0].URL)
	}
}
