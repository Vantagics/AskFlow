// Package parser provides document parsing functionality for multiple file formats.
// It uses vantagedatachat libraries (gopdf2, goword, goexcel, goppt) to extract text.
package parser

import (
	"bytes"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	gopdf "github.com/VantageDataChat/GoPDF2"
	goexcel "github.com/VantageDataChat/GoExcel"
	goppt "github.com/VantageDataChat/GoPPT"
	goword "github.com/VantageDataChat/GoWord"
)

// DocumentParser handles parsing of various document formats.
type DocumentParser struct{}

// ParseResult holds the extracted text and metadata from a parsed document.
type ParseResult struct {
	Text     string            `json:"text"`
	Metadata map[string]string `json:"metadata"`
	Images   []ImageRef        `json:"images,omitempty"`
}

// ImageRef represents an image extracted from a document.
type ImageRef struct {
	Alt  string `json:"alt"`
	URL  string `json:"url"`  // external URL or relative path
	Data []byte `json:"-"`    // raw image data (for embedded images)
}

// Parse dispatches to the correct parser based on fileType.
// Supported types: "pdf", "word", "excel", "ppt".
func (dp *DocumentParser) Parse(fileData []byte, fileType string) (*ParseResult, error) {
	switch strings.ToLower(fileType) {
	case "pdf":
		return dp.parsePDF(fileData)
	case "word":
		return dp.parseWord(fileData)
	case "excel":
		return dp.parseExcel(fileData)
	case "ppt":
		return dp.parsePPT(fileData)
	case "markdown":
		return dp.parseMarkdown(fileData)
	case "html":
		return dp.parseHTML(fileData, "")
	default:
		return nil, fmt.Errorf("不支持的文件格式: %s", fileType)
	}
}

// ParseWithBaseURL dispatches to the correct parser, passing baseURL for HTML image resolution.
func (dp *DocumentParser) ParseWithBaseURL(fileData []byte, fileType string, baseURL string) (*ParseResult, error) {
	if strings.ToLower(fileType) == "html" {
		return dp.parseHTML(fileData, baseURL)
	}
	return dp.Parse(fileData, fileType)
}

// parsePDF extracts text from PDF data using gopdf2, preserving paragraph structure.
func (dp *DocumentParser) parsePDF(data []byte) (result *ParseResult, err error) {
	defer func() {
		if r := recover(); r != nil {
			result = nil
			err = fmt.Errorf("pdf解析错误: %v", r)
		}
	}()

	pageCount, err := gopdf.GetSourcePDFPageCountFromBytes(data)
	if err != nil {
		return nil, fmt.Errorf("pdf解析错误: %w", err)
	}

	var sb strings.Builder
	for i := 0; i < pageCount; i++ {
		text, err := gopdf.ExtractPageText(data, i)
		if err != nil {
			return nil, fmt.Errorf("pdf解析错误: 第%d页提取失败: %w", i+1, err)
		}
		if text != "" {
			if sb.Len() > 0 {
				sb.WriteString("\n\n")
			}
			sb.WriteString(text)
		}
	}

	return &ParseResult{
		Text: CleanText(sb.String()),
		Metadata: map[string]string{
			"type":       "pdf",
			"page_count": fmt.Sprintf("%d", pageCount),
		},
	}, nil
}

// parseWord extracts text from Word data using goword, preserving headings and paragraphs.
func (dp *DocumentParser) parseWord(data []byte) (result *ParseResult, err error) {
	defer func() {
		if r := recover(); r != nil {
			result = nil
			err = fmt.Errorf("word解析错误: %v", r)
		}
	}()

	doc, err := goword.OpenFromBytes(data)
	if err != nil {
		return nil, fmt.Errorf("word解析错误: %w", err)
	}

	text := doc.ExtractText()

	return &ParseResult{
		Text: CleanText(text),
		Metadata: map[string]string{
			"type":  "word",
			"title": doc.Properties.Title,
		},
	}, nil
}

// parseExcel extracts cell content from Excel data using goexcel,
// organized per sheet in "SheetName-Row,Col" format.
func (dp *DocumentParser) parseExcel(data []byte) (result *ParseResult, err error) {
	defer func() {
		if r := recover(); r != nil {
			result = nil
			err = fmt.Errorf("excel解析错误: %v", r)
		}
	}()

	reader := goexcel.NewXLSXReader()
	wb, err := reader.Read(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("excel解析错误: %w", err)
	}

	var sb strings.Builder
	sheetNames := wb.GetSheetNames()
	for _, name := range sheetNames {
		sheet, err := wb.GetSheetByName(name)
		if err != nil {
			continue
		}
		rows, err := sheet.RowIterator()
		if err != nil {
			continue
		}
		for rowIdx, row := range rows {
			for _, cell := range row {
				if cell == nil || cell.IsEmpty() {
					continue
				}
				val := cell.GetFormattedValue()
				if val == "" {
					continue
				}
				if sb.Len() > 0 {
					sb.WriteString("\n")
				}
				sb.WriteString(fmt.Sprintf("%s-%d,%d: %s", name, rowIdx+1, cell.Col()+1, val))
			}
		}
	}

	return &ParseResult{
		Text: CleanText(sb.String()),
		Metadata: map[string]string{
			"type":        "excel",
			"sheet_count": fmt.Sprintf("%d", len(sheetNames)),
		},
	}, nil
}

// parsePPT extracts slide text from PowerPoint data using goppt, per page.
func (dp *DocumentParser) parsePPT(data []byte) (result *ParseResult, err error) {
	defer func() {
		if r := recover(); r != nil {
			result = nil
			err = fmt.Errorf("ppt解析错误: %v", r)
		}
	}()

	pres, err := goppt.ReadFrom(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("ppt解析错误: %w", err)
	}

	var sb strings.Builder
	slides := pres.Slides()
	for i, slide := range slides {
		text := slide.ExtractText()
		if text != "" {
			if sb.Len() > 0 {
				sb.WriteString("\n\n")
			}
			sb.WriteString(fmt.Sprintf("Slide %d:\n%s", i+1, text))
		}
	}

	return &ParseResult{
		Text: CleanText(sb.String()),
		Metadata: map[string]string{
			"type":        "ppt",
			"slide_count": fmt.Sprintf("%d", len(slides)),
		},
	}, nil
}

// CleanText removes excessive whitespace and meaningless special characters from text.
// It trims leading/trailing whitespace, collapses multiple spaces into one,
// and removes control characters (except newlines and tabs).
func CleanText(text string) string {
	// Remove control characters except \n and \t
	controlRe := regexp.MustCompile(`[\x00-\x08\x0B\x0C\x0E-\x1F\x7F]`)
	text = controlRe.ReplaceAllString(text, "")

	// Collapse multiple spaces/tabs into a single space (per line)
	lines := strings.Split(text, "\n")
	var cleaned []string
	spaceRe := regexp.MustCompile(`[ \t]+`)
	for _, line := range lines {
		line = spaceRe.ReplaceAllString(line, " ")
		line = strings.TrimSpace(line)
		cleaned = append(cleaned, line)
	}
	text = strings.Join(cleaned, "\n")

	// Collapse 3+ consecutive newlines into 2
	nlRe := regexp.MustCompile(`\n{3,}`)
	text = nlRe.ReplaceAllString(text, "\n\n")

	return strings.TrimSpace(text)
}

// parseMarkdown extracts plain text from Markdown content.
// Strips common Markdown syntax while preserving the text structure.
func (dp *DocumentParser) parseMarkdown(data []byte) (*ParseResult, error) {
	text := string(data)
	if strings.TrimSpace(text) == "" {
		return nil, fmt.Errorf("Markdown文件内容为空")
	}

	// Extract image references before stripping markdown
	reImg := regexp.MustCompile(`!\[([^\]]*)\]\(([^)]+)\)`)
	imgMatches := reImg.FindAllStringSubmatch(text, -1)
	var images []ImageRef
	for _, m := range imgMatches {
		if len(m) >= 3 {
			images = append(images, ImageRef{Alt: m[1], URL: m[2]})
		}
	}

	// Strip common markdown syntax for cleaner text
	re := regexp.MustCompile(`(?m)^#{1,6}\s+`)
	text = re.ReplaceAllString(text, "")

	text = strings.ReplaceAll(text, "**", "")
	text = strings.ReplaceAll(text, "__", "")
	text = strings.ReplaceAll(text, "*", "")
	text = strings.ReplaceAll(text, "_", "")
	text = strings.ReplaceAll(text, "`", "")

	reLink := regexp.MustCompile(`\[([^\]]+)\]\([^)]+\)`)
	text = reLink.ReplaceAllString(text, "$1")

	// Replace image syntax with alt text + image marker
	text = reImg.ReplaceAllString(text, "$1")

	reBlank := regexp.MustCompile(`\n{3,}`)
	text = reBlank.ReplaceAllString(text, "\n\n")

	return &ParseResult{
		Text:     strings.TrimSpace(text),
		Metadata: map[string]string{"format": "markdown"},
		Images:   images,
	}, nil
}

// parseHTML extracts text and images from HTML content.
// It strips HTML tags while preserving text structure, and collects <img> src URLs.
// If baseURL is provided, relative image URLs are resolved to absolute URLs.
func (dp *DocumentParser) parseHTML(data []byte, baseURL string) (*ParseResult, error) {
	html := string(data)
	if strings.TrimSpace(html) == "" {
		return nil, fmt.Errorf("HTML文件内容为空")
	}

	// Parse base URL for resolving relative image paths
	var base *url.URL
	if baseURL != "" {
		var err error
		base, err = url.Parse(baseURL)
		if err != nil {
			base = nil
		}
	}

	// Also check for <base href="..."> in the HTML
	if base == nil {
		reBase := regexp.MustCompile(`(?i)<base[^>]+href\s*=\s*["']([^"']+)["']`)
		if m := reBase.FindStringSubmatch(html); len(m) >= 2 {
			if parsed, err := url.Parse(m[1]); err == nil {
				base = parsed
			}
		}
	}

	// Extract images from <img> tags before stripping HTML
	var images []ImageRef
	reImg := regexp.MustCompile(`(?i)<img[^>]*\bsrc\s*=\s*["']([^"']+)["'][^>]*>`)
	reAlt := regexp.MustCompile(`(?i)\balt\s*=\s*["']([^"']*)["']`)
	for _, m := range reImg.FindAllStringSubmatch(html, -1) {
		if len(m) < 2 {
			continue
		}
		imgSrc := strings.TrimSpace(m[1])
		if imgSrc == "" {
			continue
		}

		// Skip data URIs (inline base64 images)
		if strings.HasPrefix(imgSrc, "data:") {
			continue
		}

		// Resolve relative URLs
		imgSrc = resolveURL(imgSrc, base)

		alt := ""
		if altMatch := reAlt.FindStringSubmatch(m[0]); len(altMatch) >= 2 {
			alt = altMatch[1]
		}
		images = append(images, ImageRef{Alt: alt, URL: imgSrc})
	}

	// --- Strip HTML to extract text ---

	// Remove <script> and <style> blocks entirely
	reScript := regexp.MustCompile(`(?is)<script[^>]*>.*?</script>`)
	html = reScript.ReplaceAllString(html, "")
	reStyle := regexp.MustCompile(`(?is)<style[^>]*>.*?</style>`)
	html = reStyle.ReplaceAllString(html, "")

	// Remove HTML comments
	reComment := regexp.MustCompile(`(?s)<!--.*?-->`)
	html = reComment.ReplaceAllString(html, "")

	// Replace block-level tags with newlines for structure preservation
	blockTags := []string{"div", "p", "br", "hr", "h1", "h2", "h3", "h4", "h5", "h6",
		"li", "tr", "blockquote", "pre", "section", "article", "header", "footer", "nav", "main"}
	for _, tag := range blockTags {
		reOpen := regexp.MustCompile(`(?i)<` + tag + `[^>]*>`)
		html = reOpen.ReplaceAllString(html, "\n")
		reClose := regexp.MustCompile(`(?i)</` + tag + `\s*>`)
		html = reClose.ReplaceAllString(html, "\n")
	}

	// Replace <br> variants
	reBr := regexp.MustCompile(`(?i)<br\s*/?\s*>`)
	html = reBr.ReplaceAllString(html, "\n")

	// Replace <td>/<th> with tab for table structure
	reTd := regexp.MustCompile(`(?i)<t[dh][^>]*>`)
	html = reTd.ReplaceAllString(html, "\t")

	// Strip all remaining HTML tags
	reTag := regexp.MustCompile(`<[^>]+>`)
	html = reTag.ReplaceAllString(html, "")

	// Decode common HTML entities
	html = decodeHTMLEntities(html)

	text := CleanText(html)
	if text == "" && len(images) == 0 {
		return nil, fmt.Errorf("HTML文件内容为空")
	}

	return &ParseResult{
		Text: text,
		Metadata: map[string]string{
			"type":        "html",
			"image_count": fmt.Sprintf("%d", len(images)),
		},
		Images: images,
	}, nil
}

// resolveURL resolves a potentially relative URL against a base URL.
// Returns the original src if base is nil or resolution fails.
func resolveURL(src string, base *url.URL) string {
	if base == nil {
		return src
	}
	// Already absolute
	if strings.HasPrefix(src, "http://") || strings.HasPrefix(src, "https://") {
		return src
	}
	// Protocol-relative
	if strings.HasPrefix(src, "//") {
		return base.Scheme + ":" + src
	}
	ref, err := url.Parse(src)
	if err != nil {
		return src
	}
	return base.ResolveReference(ref).String()
}

// decodeHTMLEntities decodes common HTML entities to their text equivalents.
func decodeHTMLEntities(s string) string {
	replacer := strings.NewReplacer(
		"&amp;", "&",
		"&lt;", "<",
		"&gt;", ">",
		"&quot;", "\"",
		"&#39;", "'",
		"&apos;", "'",
		"&nbsp;", " ",
		"&mdash;", "—",
		"&ndash;", "–",
		"&hellip;", "…",
		"&copy;", "©",
		"&reg;", "®",
		"&trade;", "™",
		"&laquo;", "«",
		"&raquo;", "»",
	)
	// Also handle numeric entities like &#123; and &#x1F;
	reNumeric := regexp.MustCompile(`&#(\d+);`)
	s = reNumeric.ReplaceAllStringFunc(s, func(match string) string {
		var n int
		fmt.Sscanf(match, "&#%d;", &n)
		if n > 0 && n < 0x110000 {
			return string(rune(n))
		}
		return match
	})
	reHex := regexp.MustCompile(`(?i)&#x([0-9a-f]+);`)
	s = reHex.ReplaceAllStringFunc(s, func(match string) string {
		var n int
		fmt.Sscanf(strings.ToLower(match), "&#x%x;", &n)
		if n > 0 && n < 0x110000 {
			return string(rune(n))
		}
		return match
	})
	return replacer.Replace(s)
}
