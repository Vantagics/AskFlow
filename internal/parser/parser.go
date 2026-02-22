// Package parser provides document parsing functionality for multiple file formats.
// It uses vantagedatachat libraries (gopdf2, goword, goexcel, goppt) to extract text.
package parser

import (
	"archive/zip"
	"bytes"
	"crypto/md5"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"log"
	"net/url"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

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
	Alt       string `json:"alt"`
	URL       string `json:"url"`       // external URL or relative path
	Data      []byte `json:"-"`         // raw image data (for embedded images)
	SlideText string `json:"slide_text,omitempty"` // per-slide text (for PPT: the text content of this slide)
}

// Parse dispatches to the correct parser based on fileType.
// Supported types: "pdf", "word", "excel", "ppt".
func (dp *DocumentParser) Parse(fileData []byte, fileType string) (*ParseResult, error) {
	switch strings.ToLower(fileType) {
	case "pdf":
		return dp.parsePDF(fileData)
	case "word":
		return dp.parseWord(fileData)
	case "word_legacy":
		return dp.parseWordLegacy(fileData)
	case "excel":
		return dp.parseExcel(fileData)
	case "excel_legacy":
		return dp.parseXLSLegacy(fileData)
	case "ppt":
		return dp.parsePPT(fileData)
	case "ppt_legacy":
		return dp.parsePPTLegacy(fileData)
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

// parsePDF extracts text and images from PDF data using GoPDF2.
func (dp *DocumentParser) parsePDF(data []byte) (result *ParseResult, err error) {
	defer func() {
		if r := recover(); r != nil {
			result = nil
			err = fmt.Errorf("pdf解析错误: %v", r)
		}
	}()

	// Validate PDF magic bytes
	if len(data) < 5 || string(data[:5]) != "%PDF-" {
		return nil, fmt.Errorf("pdf解析错误: 不是有效的PDF文件")
	}

	// Get page count
	pageCount, err := gopdf.GetSourcePDFPageCountFromBytes(data)
	if err != nil {
		return nil, fmt.Errorf("pdf解析错误: %w", err)
	}

	// Extract text page by page
	var sb strings.Builder
	for i := 0; i < pageCount; i++ {
		text, err := gopdf.ExtractPageText(data, i)
		if err != nil {
			continue
		}
		if text != "" {
			if sb.Len() > 0 {
				sb.WriteString("\n\n")
			}
			sb.WriteString(text)
		}
	}

	// Extract images (best-effort, non-fatal)
	var images []ImageRef
	func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[PDF] image extraction panic: %v", r)
			}
		}()
		imgMap, imgErr := gopdf.ExtractImagesFromAllPages(data)
		if imgErr != nil {
			log.Printf("[PDF] image extraction error: %v", imgErr)
			return
		}
		totalFound := 0
		for _, imgs := range imgMap {
			totalFound += len(imgs)
		}
		log.Printf("[PDF] found %d raw images across %d pages", totalFound, len(imgMap))

		// Detect PPT-to-PDF layer pattern: many pages each with many same-size
		// FlateDecode images that are compositing layers.
		// Instead of extracting individual (useless) layers, composite all layers
		// per page into a single complete slide image.
		if isPPTLayerPDF(imgMap, pageCount) {
			log.Printf("[PDF] detected PPT-to-PDF layer pattern (%d images across %d pages), compositing layers per page", totalFound, pageCount)
			images = compositePDFLayers(imgMap, pageCount)
			log.Printf("[PDF] layer compositing done: %d page images", len(images))
			return
		}

		// Normal PDF: deduplicate and extract individual images
		seen := make(map[[16]byte]bool)
		skippedSmall, skippedDup := 0, 0
		const maxImagesPerPage = 5

		var imgPageIndices []int
		for idx := range imgMap {
			imgPageIndices = append(imgPageIndices, idx)
		}
		sort.Ints(imgPageIndices)
		for _, pageIdx := range imgPageIndices {
			pageKept := 0
			for _, img := range imgMap[pageIdx] {
				// Skip tiny images (icons, decorations, bullets)
				if len(img.Data) == 0 || img.Width < 50 || img.Height < 50 {
					skippedSmall++
					continue
				}
				// Per-page limit
				if pageKept >= maxImagesPerPage {
					break
				}
				// Dedup by content hash
				hash := md5.Sum(img.Data)
				if seen[hash] {
					skippedDup++
					continue
				}
				seen[hash] = true

				imgData := img.Data
				// For FlateDecode images, Data is raw pixel bytes — encode to JPEG.
				// DCTDecode (JPEG) and JPXDecode (JP2) are already valid image formats.
				if img.Filter == "FlateDecode" {
					encoded := rawPixelsToJPEG(img.Data, img.Width, img.Height, img.ColorSpace)
					if encoded == nil {
						continue
					}
					imgData = encoded
				} else if img.Filter == "" {
					// Unknown filter: check if data is already a valid image format
					if isImageJPEGOrPNG(img.Data) {
						// Already encoded, use as-is
					} else {
						encoded := rawPixelsToJPEG(img.Data, img.Width, img.Height, img.ColorSpace)
						if encoded == nil {
							continue
						}
						imgData = encoded
					}
				}
				pageKept++
				images = append(images, ImageRef{
					Alt:  fmt.Sprintf("PDF第%d页图片%d", pageIdx+1, pageKept),
					Data: imgData,
				})
			}
		}
		log.Printf("[PDF] image extraction done: %d kept, %d skipped (small), %d skipped (dup)",
			len(images), skippedSmall, skippedDup)
	}()

	return &ParseResult{
		Text: CleanText(sb.String()),
		Metadata: map[string]string{
			"type":        "pdf",
			"page_count":  fmt.Sprintf("%d", pageCount),
			"image_count": fmt.Sprintf("%d", len(images)),
		},
		Images: images,
	}, nil
}

// rawPixelsToJPEG converts raw decompressed pixel data from a PDF image to JPEG.
// Handles both plain pixel data and PNG-predictor-encoded data (common in FlateDecode
// streams where the PDF uses Predictor=10..15). PNG predictor adds 1 filter-type byte
// per row, so total size = height * (width*bytesPerPixel + 1).
// Uses quality 85.
func rawPixelsToJPEG(data []byte, width, height int, colorSpace string) []byte {
	if width <= 0 || height <= 0 {
		return nil
	}

	isGray := strings.Contains(colorSpace, "Gray")
	bytesPerPixel := 3 // DeviceRGB
	if isGray {
		bytesPerPixel = 1
	}

	rowBytes := width * bytesPerPixel
	expectedPlain := rowBytes * height
	expectedPNG := (rowBytes + 1) * height // +1 for PNG filter byte per row

	// Detect PNG predictor: data has exactly 1 extra byte per row
	hasPNGPredictor := len(data) == expectedPNG && len(data) != expectedPlain

	if !hasPNGPredictor && len(data) < expectedPlain {
		return nil
	}

	// If PNG predictor, decode the filter bytes to get raw pixels
	var pixels []byte
	if hasPNGPredictor {
		pixels = decodePNGPredictor(data, width, height, bytesPerPixel)
		if pixels == nil {
			return nil
		}
	} else {
		pixels = data
	}

	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	if isGray {
		for y := 0; y < height; y++ {
			srcOff := y * width
			dstOff := y * img.Stride
			for x := 0; x < width; x++ {
				g := pixels[srcOff+x]
				img.Pix[dstOff] = g
				img.Pix[dstOff+1] = g
				img.Pix[dstOff+2] = g
				img.Pix[dstOff+3] = 255
				dstOff += 4
			}
		}
	} else {
		for y := 0; y < height; y++ {
			srcOff := y * width * 3
			dstOff := y * img.Stride
			for x := 0; x < width; x++ {
				img.Pix[dstOff] = pixels[srcOff]
				img.Pix[dstOff+1] = pixels[srcOff+1]
				img.Pix[dstOff+2] = pixels[srcOff+2]
				img.Pix[dstOff+3] = 255
				srcOff += 3
				dstOff += 4
			}
		}
	}

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 85}); err != nil {
		return nil
	}
	return buf.Bytes()
}

// decodePNGPredictor reverses PNG row filters on FlateDecode data.
// Each row starts with a filter-type byte (0=None, 1=Sub, 2=Up, 3=Average, 4=Paeth)
// followed by rowBytes of filtered pixel data.
// Returns the unfiltered raw pixel data (without filter bytes).
func decodePNGPredictor(data []byte, width, height, bytesPerPixel int) []byte {
	rowBytes := width * bytesPerPixel
	srcStride := rowBytes + 1 // +1 for filter byte
	out := make([]byte, rowBytes*height)

	for y := 0; y < height; y++ {
		srcRow := data[y*srcStride : y*srcStride+srcStride]
		filterType := srcRow[0]
		filtered := srcRow[1:]
		dstRow := out[y*rowBytes : y*rowBytes+rowBytes]

		var prevRow []byte
		if y > 0 {
			prevRow = out[(y-1)*rowBytes : (y-1)*rowBytes+rowBytes]
		}

		switch filterType {
		case 0: // None
			copy(dstRow, filtered)
		case 1: // Sub: each byte = filtered + byte at (bytesPerPixel) positions left
			for i := 0; i < rowBytes; i++ {
				left := byte(0)
				if i >= bytesPerPixel {
					left = dstRow[i-bytesPerPixel]
				}
				dstRow[i] = filtered[i] + left
			}
		case 2: // Up: each byte = filtered + byte in same position of previous row
			for i := 0; i < rowBytes; i++ {
				up := byte(0)
				if prevRow != nil {
					up = prevRow[i]
				}
				dstRow[i] = filtered[i] + up
			}
		case 3: // Average: each byte = filtered + floor((left + up) / 2)
			for i := 0; i < rowBytes; i++ {
				left := 0
				if i >= bytesPerPixel {
					left = int(dstRow[i-bytesPerPixel])
				}
				up := 0
				if prevRow != nil {
					up = int(prevRow[i])
				}
				dstRow[i] = filtered[i] + byte((left+up)/2)
			}
		case 4: // Paeth
			for i := 0; i < rowBytes; i++ {
				left := byte(0)
				if i >= bytesPerPixel {
					left = dstRow[i-bytesPerPixel]
				}
				up := byte(0)
				if prevRow != nil {
					up = prevRow[i]
				}
				upLeft := byte(0)
				if prevRow != nil && i >= bytesPerPixel {
					upLeft = prevRow[i-bytesPerPixel]
				}
				dstRow[i] = filtered[i] + paethPredictor(left, up, upLeft)
			}
		default:
			// Unknown filter type — treat as no filter
			copy(dstRow, filtered)
		}
	}
	return out
}

// paethPredictor implements the Paeth predictor algorithm used in PNG filtering.
func paethPredictor(a, b, c byte) byte {
	ia, ib, ic := int(a), int(b), int(c)
	p := ia + ib - ic
	pa := p - ia
	if pa < 0 {
		pa = -pa
	}
	pb := p - ib
	if pb < 0 {
		pb = -pb
	}
	pc := p - ic
	if pc < 0 {
		pc = -pc
	}
	if pa <= pb && pa <= pc {
		return a
	}
	if pb <= pc {
		return b
	}
	return c
}


// parseWord extracts text and images from Word (.docx) data.
// Text is extracted via goword; images are extracted directly from the DOCX ZIP (word/media/).
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

	// Extract embedded images from DOCX ZIP (word/media/*)
	// GoWord's reader doesn't populate Images(), so we read the ZIP directly.
	var images []ImageRef
	zr, zipErr := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if zipErr == nil {
		imgIdx := 0
		for _, f := range zr.File {
			// Images are stored under word/media/ (e.g. word/media/image1.png)
			if !strings.HasPrefix(f.Name, "word/media/") {
				continue
			}
			rc, err := f.Open()
			if err != nil {
				continue
			}
			imgData, err := io.ReadAll(io.LimitReader(rc, 20<<20)) // 20MB max per image
			rc.Close()
			if err != nil || len(imgData) == 0 {
				continue
			}
			if !isImageJPEGOrPNG(imgData) {
				ext := strings.ToLower(filepath.Ext(f.Name))
				// Try EMF/WMF metafile conversion (reuse legacy.go logic)
				if ext == ".emf" || ext == ".wmf" {
					converted := convertMetafileImage(imgData, ext)
					if converted != nil {
						imgIdx++
						images = append(images, ImageRef{
							Alt:  fmt.Sprintf("Word图片%d", imgIdx),
							Data: converted,
						})
					} else {
						log.Printf("[Word] failed to convert %s", f.Name)
					}
				} else {
					log.Printf("[Word] skipping %s: unsupported format (ext=%s)", f.Name, ext)
				}
				continue
			}
			imgIdx++
			images = append(images, ImageRef{
				Alt:  fmt.Sprintf("Word图片%d", imgIdx),
				Data: imgData,
			})
		}
	}
	log.Printf("[Word] extracted %d images", len(images))

	return &ParseResult{
		Text: CleanText(text),
		Metadata: map[string]string{
			"type":        "word",
			"title":       doc.Properties.Title,
			"image_count": fmt.Sprintf("%d", len(images)),
		},
		Images: images,
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

// parsePPT extracts slide text and renders each slide as an image.
// Uses GoPPT's SlidesToImages to batch-render all slides as PNG images,
// with a shared FontCache for consistent font rendering and better performance.
func (dp *DocumentParser) parsePPT(data []byte) (result *ParseResult, err error) {
	defer func() {
		if r := recover(); r != nil {
			result = nil
			err = fmt.Errorf("ppt解析错误: %v", r)
		}
	}()

	log.Printf("[PPT] Starting PPT parsing, data size: %d bytes", len(data))

	pres, err := goppt.ReadFrom(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		log.Printf("[PPT] Failed to read PPT: %v", err)
		return nil, fmt.Errorf("ppt解析错误: %w", err)
	}
	defer pres.Close()

	slides := pres.Slides()
	log.Printf("[PPT] Found %d slides", len(slides))
	var sb strings.Builder

	// Extract text from all slides first
	slideTexts := make([]string, len(slides))
	for i, slide := range slides {
		text := slide.ExtractText()
		slideTexts[i] = text
		if text != "" {
			if sb.Len() > 0 {
				sb.WriteString("\n\n")
			}
			sb.WriteString(fmt.Sprintf("Slide %d:\n%s", i+1, text))
		}
	}
	log.Printf("[PPT] Text extraction completed")

	// Batch render all slides with shared FontCache
	opts := goppt.DefaultRenderOptions()
	opts.Width = 1280
	opts.FontCache = goppt.NewFontCache()

	log.Printf("[PPT] Starting batch slide rendering...")
	renderedImages, renderErr := pres.SlidesToImages(opts)
	if renderErr != nil {
		log.Printf("Warning: PPT批量渲染失败，逐页重试: %v", renderErr)
	} else {
		log.Printf("[PPT] Batch rendering completed, got %d images", len(renderedImages))
	}

	var images []ImageRef
	for i := range slides {
		var img image.Image
		if renderErr == nil && i < len(renderedImages) {
			img = renderedImages[i]
		} else {
			// Fallback: render individual slide
			log.Printf("[PPT] Rendering slide %d individually...", i+1)
			singleImg, sErr := pres.SlideToImage(i, opts)
			if sErr != nil {
				log.Printf("Warning: PPT第%d页渲染失败: %v", i+1, sErr)
				continue
			}
			img = singleImg
		}

		var buf bytes.Buffer
		if err := png.Encode(&buf, img); err != nil {
			log.Printf("Warning: PPT第%d页PNG编码失败: %v", i+1, err)
			continue
		}

		text := slideTexts[i]
		alt := fmt.Sprintf("PPT第%d页", i+1)
		if text != "" {
			t := strings.TrimSpace(text)
			if len(t) > 200 {
				t = t[:200] + "..."
			}
			alt = fmt.Sprintf("PPT第%d页: %s", i+1, t)
		}

		images = append(images, ImageRef{
			Alt:       alt,
			Data:      buf.Bytes(),
			SlideText: strings.TrimSpace(text),
		})
	}

	log.Printf("[PPT] PPT parsing completed: %d slides, %d images", len(slides), len(images))

	return &ParseResult{
		Text: CleanText(sb.String()),
		Metadata: map[string]string{
			"type":        "ppt",
			"slide_count": fmt.Sprintf("%d", len(slides)),
			"image_count": fmt.Sprintf("%d", len(images)),
		},
		Images: images,
	}, nil
}

// Pre-compiled regexes for CleanText to avoid recompilation on every call.
var (
	controlCharRe    = regexp.MustCompile(`[\x00-\x08\x0B\x0C\x0E-\x1F\x7F]`)
	multiSpaceRe     = regexp.MustCompile(`[ \t]+`)
	multiNewlineRe   = regexp.MustCompile(`\n{3,}`)
)

// Pre-compiled regexes for parseMarkdown.
var (
	mdImgRe        = regexp.MustCompile(`!\[([^\]]*)\]\(([^)]+)\)`)
	mdHeadingRe    = regexp.MustCompile(`(?m)^#{1,6}\s+`)
	mdBoldRe       = regexp.MustCompile(`\*\*(.+?)\*\*`)
	mdUnderBoldRe  = regexp.MustCompile(`__(.+?)__`)
	mdItalicRe     = regexp.MustCompile(`\*(.+?)\*`)
	mdUnderItalicRe = regexp.MustCompile(`_(.+?)_`)
	mdCodeRe       = regexp.MustCompile("`([^`]+)`")
	mdLinkRe       = regexp.MustCompile(`\[([^\]]+)\]\([^)]+\)`)
)

// Pre-compiled regexes for parseHTML.
var (
	htmlBaseRe    = regexp.MustCompile(`(?i)<base[^>]+href\s*=\s*["']([^"']+)["']`)
	htmlImgRe     = regexp.MustCompile(`(?i)<img[^>]*\bsrc\s*=\s*["']([^"']+)["'][^>]*>`)
	htmlAltRe     = regexp.MustCompile(`(?i)\balt\s*=\s*["']([^"']*)["']`)
	htmlScriptRe  = regexp.MustCompile(`(?is)<script[^>]*>.*?</script>`)
	htmlStyleRe   = regexp.MustCompile(`(?is)<style[^>]*>.*?</style>`)
	htmlCommentRe = regexp.MustCompile(`(?s)<!--.*?-->`)
	htmlBrRe      = regexp.MustCompile(`(?i)<br\s*/?\s*>`)
	htmlTdRe      = regexp.MustCompile(`(?i)<t[dh][^>]*>`)
	htmlTagRe     = regexp.MustCompile(`<[^>]+>`)
)

// Pre-compiled block tag regexes for parseHTML.
var blockTags = []string{"div", "p", "br", "hr", "h1", "h2", "h3", "h4", "h5", "h6",
	"li", "tr", "blockquote", "pre", "section", "article", "header", "footer", "nav", "main"}

var (
	blockOpenRe  = make(map[string]*regexp.Regexp)
	blockCloseRe = make(map[string]*regexp.Regexp)
)

func init() {
	for _, tag := range blockTags {
		blockOpenRe[tag] = regexp.MustCompile(`(?i)<` + tag + `[^>]*>`)
		blockCloseRe[tag] = regexp.MustCompile(`(?i)</` + tag + `\s*>`)
	}
}

// isImageJPEGOrPNG checks if the data starts with JPEG or PNG magic bytes.
func isImageJPEGOrPNG(data []byte) bool {
	if len(data) >= 3 && data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF {
		return true // JPEG
	}
	if len(data) >= 4 && string(data[:4]) == "\x89PNG" {
		return true // PNG
	}
	return false
}

// isPPTLayerPDF detects PDFs generated from PPT/Keynote where each page contains
// many same-size FlateDecode image layers (compositing layers). These layers are
// mostly transparent and useless when extracted individually — they produce
// black-background images with only partial content (text layer, chart layer, etc.).
//
// Detection heuristic: if most pages have 10+ images that are all the same
// dimensions and all FlateDecode, it's almost certainly a PPT-to-PDF conversion.
func isPPTLayerPDF(imgMap map[int][]gopdf.ExtractedImage, pageCount int) bool {
	if pageCount < 2 || len(imgMap) < 2 {
		return false
	}

	// Count pages that have many same-size FlateDecode images
	layerPages := 0
	for _, imgs := range imgMap {
		if len(imgs) < 10 {
			continue
		}
		// Check if all large images on this page share the same dimensions
		var refW, refH int
		sameSize := 0
		for _, img := range imgs {
			if img.Width < 50 || img.Height < 50 || len(img.Data) == 0 {
				continue
			}
			if refW == 0 {
				refW, refH = img.Width, img.Height
			}
			if img.Width == refW && img.Height == refH && img.Filter == "FlateDecode" {
				sameSize++
			}
		}
		if sameSize >= 10 {
			layerPages++
		}
	}

	// If >50% of pages match the layer pattern, it's a PPT-to-PDF
	return layerPages > len(imgMap)/2
}

// compositePDFLayers composites all FlateDecode image layers on each page into
// a single complete image per page. PPT-to-PDF conversions store each slide as
// multiple compositing layers (background, charts, text, decorations, etc.) that
// must be overlaid to reconstruct the full slide.
//
// Strategy: decode each layer's raw pixels, then paint non-black pixels on top
// of the canvas. Black (0,0,0) is treated as transparent since these layers use
// black as the "empty" color. Pages are processed concurrently for performance.
func compositePDFLayers(imgMap map[int][]gopdf.ExtractedImage, pageCount int) []ImageRef {
	var pageIndices []int
	for idx := range imgMap {
		pageIndices = append(pageIndices, idx)
	}
	sort.Ints(pageIndices)

	type pageResult struct {
		index int
		ref   ImageRef
	}

	resultCh := make(chan pageResult, len(pageIndices))
	var wg sync.WaitGroup

	// Process pages concurrently (limit to 4 workers to control memory usage,
	// each page canvas is ~14MB for 2400x1500 NRGBA)
	sem := make(chan struct{}, 4)

	for _, pageIdx := range pageIndices {
		wg.Add(1)
		go func(pIdx int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			ref := compositeOnePage(imgMap[pIdx], pIdx)
			if ref != nil {
				resultCh <- pageResult{index: pIdx, ref: *ref}
			}
		}(pageIdx)
	}

	go func() {
		wg.Wait()
		close(resultCh)
	}()

	// Collect and sort by page index
	var collected []pageResult
	for pr := range resultCh {
		collected = append(collected, pr)
	}
	sort.Slice(collected, func(i, j int) bool {
		return collected[i].index < collected[j].index
	})

	results := make([]ImageRef, len(collected))
	for i, c := range collected {
		results[i] = c.ref
	}
	return results
}

// compositeOnePage composites all layers of a single PDF page into one image.
func compositeOnePage(imgs []gopdf.ExtractedImage, pageIdx int) *ImageRef {
	if len(imgs) == 0 {
		return nil
	}

	// Find the reference dimensions
	var refW, refH int
	for _, img := range imgs {
		if img.Width > 0 && img.Height > 0 && len(img.Data) > 0 {
			refW, refH = img.Width, img.Height
			break
		}
	}
	if refW == 0 || refH == 0 {
		return nil
	}

	// Canvas: NRGBA initialized to white
	canvas := image.NewNRGBA(image.Rect(0, 0, refW, refH))
	pix := canvas.Pix
	for i := 0; i < len(pix); i += 4 {
		pix[i] = 255
		pix[i+1] = 255
		pix[i+2] = 255
		pix[i+3] = 255
	}

	layersComposited := 0
	stride := canvas.Stride

	for _, img := range imgs {
		if img.Width != refW || img.Height != refH || len(img.Data) == 0 || img.Filter != "FlateDecode" {
			continue
		}

		isGray := strings.Contains(img.ColorSpace, "Gray")
		bpp := 3
		if isGray {
			bpp = 1
		}
		rowBytes := refW * bpp
		expectedPNG := (rowBytes + 1) * refH
		expectedPlain := rowBytes * refH
		hasPNG := len(img.Data) == expectedPNG && len(img.Data) != expectedPlain

		var pixels []byte
		if hasPNG {
			pixels = decodePNGPredictor(img.Data, refW, refH, bpp)
			if pixels == nil {
				continue
			}
		} else if len(img.Data) >= expectedPlain {
			pixels = img.Data
		} else {
			continue
		}

		// Composite: paint non-black pixels onto canvas
		const blackThreshold = 10
		if isGray {
			for y := 0; y < refH; y++ {
				srcOff := y * refW
				dstOff := y * stride
				for x := 0; x < refW; x++ {
					g := pixels[srcOff+x]
					if g > blackThreshold {
						pix[dstOff] = g
						pix[dstOff+1] = g
						pix[dstOff+2] = g
					}
					dstOff += 4
				}
			}
		} else {
			for y := 0; y < refH; y++ {
				srcOff := y * refW * 3
				dstOff := y * stride
				for x := 0; x < refW; x++ {
					r, g, b := pixels[srcOff], pixels[srcOff+1], pixels[srcOff+2]
					if r > blackThreshold || g > blackThreshold || b > blackThreshold {
						pix[dstOff] = r
						pix[dstOff+1] = g
						pix[dstOff+2] = b
					}
					srcOff += 3
					dstOff += 4
				}
			}
		}
		layersComposited++
	}

	if layersComposited == 0 {
		return nil
	}

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, canvas, &jpeg.Options{Quality: 85}); err != nil {
		log.Printf("[PDF] failed to encode composited page %d: %v", pageIdx+1, err)
		return nil
	}

	return &ImageRef{
		Alt:  fmt.Sprintf("PDF第%d页", pageIdx+1),
		Data: buf.Bytes(),
	}
}

// convertMetafileImage attempts to extract a raster image from EMF/WMF metafile data.
// Reuses the extraction logic from legacy.go (findEmbeddedRaster, extractDIBFromEMF/WMF).
func convertMetafileImage(data []byte, ext string) []byte {
	// Strategy 1: look for embedded JPEG/PNG inside the metafile
	if img := findEmbeddedRaster(data); img != nil {
		return img
	}
	// Strategy 2: parse EMF/WMF records for DIB data and convert to PNG
	if ext == ".emf" {
		return extractDIBFromEMF(data)
	}
	if ext == ".wmf" {
		return extractDIBFromWMF(data)
	}
	return nil
}

// CleanText removes excessive whitespace and meaningless special characters from text.
// It trims leading/trailing whitespace, collapses multiple spaces into one,
// and removes control characters (except newlines and tabs).
func CleanText(text string) string {
	// Remove control characters except \n and \t
	text = controlCharRe.ReplaceAllString(text, "")

	// Collapse multiple spaces/tabs into a single space (per line)
	lines := strings.Split(text, "\n")
	var cleaned []string
	for _, line := range lines {
		line = multiSpaceRe.ReplaceAllString(line, " ")
		line = strings.TrimSpace(line)
		cleaned = append(cleaned, line)
	}
	text = strings.Join(cleaned, "\n")

	// Collapse 3+ consecutive newlines into 2
	text = multiNewlineRe.ReplaceAllString(text, "\n\n")

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
	imgMatches := mdImgRe.FindAllStringSubmatch(text, -1)
	var images []ImageRef
	for _, m := range imgMatches {
		if len(m) >= 3 {
			images = append(images, ImageRef{Alt: m[1], URL: m[2]})
		}
	}

	// Strip common markdown syntax for cleaner text
	text = mdHeadingRe.ReplaceAllString(text, "")
	text = mdBoldRe.ReplaceAllString(text, "$1")
	text = mdUnderBoldRe.ReplaceAllString(text, "$1")
	text = mdItalicRe.ReplaceAllString(text, "$1")
	text = mdUnderItalicRe.ReplaceAllString(text, "$1")
	text = mdCodeRe.ReplaceAllString(text, "$1")
	text = mdLinkRe.ReplaceAllString(text, "$1")

	// Replace image syntax with alt text
	text = mdImgRe.ReplaceAllString(text, "$1")

	text = multiNewlineRe.ReplaceAllString(text, "\n\n")

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
		if m := htmlBaseRe.FindStringSubmatch(html); len(m) >= 2 {
			if parsed, err := url.Parse(m[1]); err == nil {
				base = parsed
			}
		}
	}

	// Extract images from <img> tags before stripping HTML
	var images []ImageRef
	for _, m := range htmlImgRe.FindAllStringSubmatch(html, -1) {
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
		if altMatch := htmlAltRe.FindStringSubmatch(m[0]); len(altMatch) >= 2 {
			alt = altMatch[1]
		}
		images = append(images, ImageRef{Alt: alt, URL: imgSrc})
	}

	// --- Strip HTML to extract text ---

	// Remove <script> and <style> blocks entirely
	html = htmlScriptRe.ReplaceAllString(html, "")
	html = htmlStyleRe.ReplaceAllString(html, "")

	// Remove HTML comments
	html = htmlCommentRe.ReplaceAllString(html, "")

	// Replace block-level tags with newlines for structure preservation
	for _, tag := range blockTags {
		html = blockOpenRe[tag].ReplaceAllString(html, "\n")
		html = blockCloseRe[tag].ReplaceAllString(html, "\n")
	}

	// Replace <br> variants
	html = htmlBrRe.ReplaceAllString(html, "\n")

	// Replace <td>/<th> with tab for table structure
	html = htmlTdRe.ReplaceAllString(html, "\t")

	// Strip all remaining HTML tags
	html = htmlTagRe.ReplaceAllString(html, "")

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

// Pre-compiled regexes for decodeHTMLEntities.
var (
	reNumericEntity = regexp.MustCompile(`&#(\d+);`)
	reHexEntity     = regexp.MustCompile(`(?i)&#x([0-9a-f]+);`)
)

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
	s = reNumericEntity.ReplaceAllStringFunc(s, func(match string) string {
		var n int
		fmt.Sscanf(match, "&#%d;", &n)
		if n > 0 && n < 0x110000 {
			return string(rune(n))
		}
		return match
	})
	s = reHexEntity.ReplaceAllStringFunc(s, func(match string) string {
		var n int
		fmt.Sscanf(strings.ToLower(match), "&#x%x;", &n)
		if n > 0 && n < 0x110000 {
			return string(rune(n))
		}
		return match
	})
	return replacer.Replace(s)
}
