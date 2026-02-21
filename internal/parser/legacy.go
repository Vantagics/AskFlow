// Package parser — legacy format support for .xls, .doc, and .ppt files.
// Uses shakinm/xlsReader for .xls and richardlehane/mscfb for .doc/.ppt (OLE2).
package parser

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"strings"
	"unicode/utf16"

	"github.com/richardlehane/mscfb"
	"github.com/shakinm/xlsReader/xls"
)

// minImageSize is the minimum image data size (1KB) for extracted images.
// Images smaller than this threshold are filtered out as likely icons/bullets.
const minImageSize = 1024

// parseXLSLegacy extracts text from legacy .xls (BIFF) files using xlsReader.
func (dp *DocumentParser) parseXLSLegacy(data []byte) (result *ParseResult, err error) {
	defer func() {
		if r := recover(); r != nil {
			result = nil
			err = fmt.Errorf("xls解析错误: %v", r)
		}
	}()

	wb, err := xls.OpenReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("xls解析错误: %w", err)
	}

	var sb strings.Builder
	numSheets := wb.GetNumberSheets()
	for i := 0; i < numSheets; i++ {
		sheet, err := wb.GetSheet(i)
		if err != nil {
			continue
		}
		sheetName := sheet.GetName()
		numRows := sheet.GetNumberRows()
		for rowIdx := 0; rowIdx < numRows; rowIdx++ {
			row, err := sheet.GetRow(rowIdx)
			if err != nil || row == nil {
				continue
			}
			cols := row.GetCols()
			for colIdx, cell := range cols {
				val := strings.TrimSpace(cell.GetString())
				if val == "" {
					continue
				}
				if sb.Len() > 0 {
					sb.WriteString("\n")
				}
				sb.WriteString(fmt.Sprintf("%s-%d,%d: %s", sheetName, rowIdx+1, colIdx+1, val))
			}
		}
	}

	text := CleanText(sb.String())
	if text == "" {
		return nil, fmt.Errorf("xls文件内容为空")
	}

	return &ParseResult{
		Text: text,
		Metadata: map[string]string{
			"type":        "excel",
			"format":      "xls_legacy",
			"sheet_count": fmt.Sprintf("%d", numSheets),
		},
	}, nil
}


// parseWordLegacy extracts text from legacy .doc files using mscfb (OLE2).
// It reads the "WordDocument" stream and extracts text from the binary content.
func (dp *DocumentParser) parseWordLegacy(data []byte) (result *ParseResult, err error) {
	defer func() {
		if r := recover(); r != nil {
			result = nil
			err = fmt.Errorf("doc解析错误: %v", r)
		}
	}()

	doc, err := mscfb.New(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("doc解析错误: %w", err)
	}

	// Look for the WordDocument and 0Table/1Table streams
	var wordDocData []byte
	var tableData []byte
	var tableName string
	var docDataStream []byte

	for {
		entry, nextErr := doc.Next()
		if nextErr != nil {
			break
		}
		switch entry.Name {
		case "WordDocument":
			wordDocData, _ = io.ReadAll(entry)
		case "0Table":
			if tableData == nil {
				tableData, _ = io.ReadAll(entry)
				tableName = entry.Name
			}
		case "1Table":
			tableData, _ = io.ReadAll(entry)
			tableName = entry.Name
		case "Data":
			docDataStream, _ = io.ReadAll(entry)
		}
	}

	if len(wordDocData) == 0 {
		return nil, fmt.Errorf("doc解析错误: 未找到WordDocument流")
	}

	text := extractWordText(wordDocData, tableData, tableName)
	text = filterWordFieldCodes(text)
	text = CleanText(text)
	if text == "" {
		return nil, fmt.Errorf("doc文件内容为空")
	}

	// 图片提取（独立 recover）
	var images []ImageRef
	if len(docDataStream) > 0 {
		func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("Warning: DOC图片提取panic: %v", r)
					images = nil
				}
			}()
			images = extractDocImages(docDataStream)
		}()
	}

	return &ParseResult{
		Text:   text,
		Images: images,
		Metadata: map[string]string{
			"type":        "word",
			"format":      "doc_legacy",
			"image_count": fmt.Sprintf("%d", len(images)),
		},
	}, nil
}


// extractWordText extracts text from a Word binary document.
// It reads the FIB (File Information Block) to locate the text in the
// WordDocument stream or the piece table in the Table stream.
func extractWordText(wordDoc []byte, tableData []byte, tableName string) string {
	if len(wordDoc) < 12 {
		return ""
	}

	// Read FIB fields
	// Offset 0x000A: flags (bit 9 = fWhichTblStm: 0=0Table, 1=1Table)
	flags := binary.LittleEndian.Uint16(wordDoc[0x0A:0x0C])
	whichTable := (flags >> 9) & 1

	// Verify table stream matches
	if tableName != "" {
		expectedTable := "0Table"
		if whichTable == 1 {
			expectedTable = "1Table"
		}
		if tableName != expectedTable && tableData != nil {
			// Wrong table stream, try to use it anyway
			_ = expectedTable
		}
	}

	// Try piece table approach first (more reliable for complex documents)
	if len(tableData) > 0 {
		if text := extractFromPieceTable(wordDoc, tableData); text != "" {
			return text
		}
	}

	// Fallback: extract text directly from WordDocument stream
	return extractDirectText(wordDoc)
}

// extractFromPieceTable reads the CLX (piece table) from the Table stream
// to extract text from the WordDocument stream.
func extractFromPieceTable(wordDoc []byte, tableData []byte) string {
	if len(wordDoc) < 0x01A2+4 {
		return ""
	}

	// FIB offset 0x01A2: fcClx (offset of CLX in table stream)
	// FIB offset 0x01A6: lcbClx (size of CLX)
	fcClx := binary.LittleEndian.Uint32(wordDoc[0x01A2:0x01A6])
	lcbClx := binary.LittleEndian.Uint32(wordDoc[0x01A6:0x01AA])

	if fcClx == 0 || lcbClx == 0 || int(fcClx+lcbClx) > len(tableData) {
		return ""
	}

	clx := tableData[fcClx : fcClx+lcbClx]

	// Find the Pcdt (piece table descriptor) in the CLX
	// Skip any Prc (property) entries (type 0x01)
	pos := 0
	for pos < len(clx) {
		if clx[pos] == 0x01 {
			// Prc: skip
			if pos+3 > len(clx) {
				break
			}
			cbGrpprl := int(binary.LittleEndian.Uint16(clx[pos+1 : pos+3]))
			pos += 3 + cbGrpprl
		} else if clx[pos] == 0x02 {
			// Pcdt found
			pos++
			break
		} else {
			break
		}
	}

	if pos >= len(clx) || pos+4 > len(clx) {
		return ""
	}

	// Read lcb (size of PlcPcd)
	lcb := int(binary.LittleEndian.Uint32(clx[pos : pos+4]))
	pos += 4

	if pos+lcb > len(clx) || lcb < 12 {
		return ""
	}

	plcPcd := clx[pos : pos+lcb]

	// PlcPcd structure: array of CPs (n+1 uint32s) followed by array of PCDs (n * 8 bytes)
	// Each PCD is 8 bytes
	pcdSize := 8
	// n = (lcb - 4) / (4 + pcdSize)
	n := (lcb - 4) / (4 + pcdSize)
	if n <= 0 {
		return ""
	}

	// Verify: (n+1)*4 + n*8 should equal lcb
	cpArraySize := (n + 1) * 4
	if cpArraySize+n*pcdSize > lcb {
		return ""
	}

	var sb strings.Builder
	for i := 0; i < n; i++ {
		cpStart := binary.LittleEndian.Uint32(plcPcd[i*4 : i*4+4])
		cpEnd := binary.LittleEndian.Uint32(plcPcd[(i+1)*4 : (i+1)*4+4])

		pcdOffset := cpArraySize + i*pcdSize
		if pcdOffset+8 > len(plcPcd) {
			break
		}

		// PCD structure: 2 bytes (flags) + 4 bytes (fc) + 2 bytes (prm)
		fcCompressed := binary.LittleEndian.Uint32(plcPcd[pcdOffset+2 : pcdOffset+6])

		isUnicode := (fcCompressed & 0x40000000) == 0
		fc := fcCompressed & 0x3FFFFFFF

		charCount := cpEnd - cpStart
		if charCount == 0 || charCount > 1000000 {
			continue
		}

		if isUnicode {
			// Unicode: each character is 2 bytes
			byteOffset := fc
			byteLen := charCount * 2
			if int(byteOffset+byteLen) > len(wordDoc) {
				continue
			}
			chunk := wordDoc[byteOffset : byteOffset+byteLen]
			u16s := make([]uint16, charCount)
			for j := uint32(0); j < charCount; j++ {
				u16s[j] = binary.LittleEndian.Uint16(chunk[j*2 : j*2+2])
			}
			runes := utf16.Decode(u16s)
			for _, r := range runes {
				if r == 0x0D || r == 0x0B {
					sb.WriteByte('\n')
				} else if r == 0x07 {
					sb.WriteByte('\t') // table cell marker
				} else if r >= 0x20 || r == 0x09 {
					sb.WriteRune(r)
				}
			}
		} else {
			// ANSI: each character is 1 byte, fc is divided by 2
			byteOffset := fc / 2
			if int(byteOffset+charCount) > len(wordDoc) {
				continue
			}
			chunk := wordDoc[byteOffset : byteOffset+charCount]
			for _, b := range chunk {
				if b == 0x0D || b == 0x0B {
					sb.WriteByte('\n')
				} else if b == 0x07 {
					sb.WriteByte('\t')
				} else if b >= 0x20 || b == 0x09 {
					sb.WriteByte(b)
				}
			}
		}
	}

	return sb.String()
}

// extractDirectText is a fallback that scans the WordDocument stream for
// readable text sequences. Less accurate but works when piece table parsing fails.
func extractDirectText(wordDoc []byte) string {
	var sb strings.Builder
	// Try to find text by scanning for printable character sequences
	// This is a best-effort fallback
	inText := false
	for i := 0; i < len(wordDoc); i++ {
		b := wordDoc[i]
		if (b >= 0x20 && b < 0x7F) || b == 0x0A || b == 0x0D || b == 0x09 {
			if b == 0x0D {
				sb.WriteByte('\n')
			} else {
				sb.WriteByte(b)
			}
			inText = true
		} else {
			if inText && sb.Len() > 0 {
				// Add separator between text blocks
				last := sb.String()
				if len(last) > 0 && last[len(last)-1] != '\n' {
					sb.WriteByte('\n')
				}
			}
			inText = false
		}
	}
	return sb.String()
}

// wordFieldCodePatterns contains Word field code markers that should be filtered.
var wordFieldCodePatterns = []string{
	"HYPERLINK",
	"PAGEREF",
	"MERGEFORMAT",
	"TOC \\o",
	"TOC \\h",
	"\\l \"",
	" \\h",
}

// filterWordFieldCodes removes lines containing Word field codes from extracted text.
// Field codes like HYPERLINK, PAGEREF, TOC, MERGEFORMAT are internal Word markers
// that leak through the piece table extraction and add noise to the content.
func filterWordFieldCodes(text string) string {
	lines := strings.Split(text, "\n")
	var filtered []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			filtered = append(filtered, line)
			continue
		}
		isFieldCode := false
		for _, pat := range wordFieldCodePatterns {
			if strings.Contains(trimmed, pat) {
				isFieldCode = true
				break
			}
		}
		if !isFieldCode {
			filtered = append(filtered, line)
		}
	}
	return strings.Join(filtered, "\n")
}

// parsePPTLegacy extracts text from legacy .ppt files using mscfb (OLE2).
// It reads the "PowerPoint Document" stream and extracts text records.
func (dp *DocumentParser) parsePPTLegacy(data []byte) (result *ParseResult, err error) {
	defer func() {
		if r := recover(); r != nil {
			result = nil
			err = fmt.Errorf("ppt解析错误: %v", r)
		}
	}()

	doc, err := mscfb.New(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("ppt解析错误: %w", err)
	}

	var pptData []byte
	var picturesData []byte
	for {
		entry, nextErr := doc.Next()
		if nextErr != nil {
			break
		}
		if entry.Name == "PowerPoint Document" {
			pptData, _ = io.ReadAll(entry)
		} else if entry.Name == "Pictures" {
			picturesData, _ = io.ReadAll(entry)
		}
	}

	if len(pptData) == 0 {
		return nil, fmt.Errorf("ppt解析错误: 未找到PowerPoint Document流")
	}

	text := extractPPTText(pptData)
	text = CleanText(text)
	if text == "" {
		return nil, fmt.Errorf("ppt文件内容为空")
	}

	// 图片提取（独立 recover）
	var images []ImageRef
	if len(picturesData) > 0 {
		func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("Warning: PPT图片提取panic: %v", r)
					images = nil
				}
			}()
			images = extractPPTImages(picturesData)
		}()
	}

	return &ParseResult{
		Text:   text,
		Images: images,
		Metadata: map[string]string{
			"type":        "ppt",
			"format":      "ppt_legacy",
			"image_count": fmt.Sprintf("%d", len(images)),
		},
	}, nil
}

// pptNoisePatterns contains master slide template placeholders and other noise
// commonly found in legacy PPT files that should be filtered out.
var pptNoisePatterns = []string{
	"单击此处编辑母版",
	"单击此处编辑母版标题样式",
	"单击此处编辑母版文本样式",
	"单击此处编辑母版副标题样式",
	"Click to edit Master title style",
	"Click to edit Master text styles",
	"Click to edit Master subtitle style",
}

// pptNoiseExact contains short strings that are exact-match noise from master slides.
var pptNoiseExact = map[string]bool{
	"*":   true,
	"二级": true,
	"三级": true,
	"四级": true,
	"五级": true,
	"Second level": true,
	"Third level":  true,
	"Fourth level": true,
	"Fifth level":  true,
}

// isPPTNoise returns true if the text line is master slide template noise.
func isPPTNoise(text string) bool {
	if pptNoiseExact[text] {
		return true
	}
	for _, pat := range pptNoisePatterns {
		if strings.Contains(text, pat) {
			return true
		}
	}
	return false
}

// extractPPTText parses the PowerPoint Document binary stream to extract text.
// PPT binary format uses record headers: recVer(4bits) + recInstance(12bits) + recType(16bits) + recLen(32bits)
// Text is stored in TextBytesAtom (type 0x0FA8) and TextCharsAtom (type 0x0FA0).
// Master slide template placeholders are filtered out.
func extractPPTText(data []byte) string {
	var sb strings.Builder
	pos := 0

	for pos+8 <= len(data) {
		// Record header: 8 bytes
		recVerInstance := binary.LittleEndian.Uint16(data[pos : pos+2])
		recType := binary.LittleEndian.Uint16(data[pos+2 : pos+4])
		recLen := binary.LittleEndian.Uint32(data[pos+4 : pos+8])

		recVer := recVerInstance & 0x0F
		_ = recVer

		pos += 8

		if recLen > uint32(len(data)-pos) {
			break
		}

		switch recType {
		case 0x0FA0: // TextCharsAtom — UTF-16LE text
			if recLen >= 2 {
				charCount := recLen / 2
				u16s := make([]uint16, charCount)
				for i := uint32(0); i < charCount; i++ {
					u16s[i] = binary.LittleEndian.Uint16(data[pos+int(i*2) : pos+int(i*2+2)])
				}
				runes := utf16.Decode(u16s)
				text := string(runes)
				text = strings.TrimSpace(text)
				if text != "" && !isPPTNoise(text) {
					if sb.Len() > 0 {
						sb.WriteString("\n")
					}
					sb.WriteString(text)
				}
			}
			pos += int(recLen)

		case 0x0FA8: // TextBytesAtom — ANSI text
			if recLen > 0 {
				text := strings.TrimSpace(string(data[pos : pos+int(recLen)]))
				if text != "" && !isPPTNoise(text) {
					if sb.Len() > 0 {
						sb.WriteString("\n")
					}
					sb.WriteString(text)
				}
			}
			pos += int(recLen)

		default:
			// Container records (recVer == 0x0F) contain sub-records, so we
			// descend into them by not skipping recLen.
			if recVer == 0x0F {
				// Don't skip — sub-records will be parsed in the next iteration
			} else {
				pos += int(recLen)
			}
		}
	}

	return sb.String()
}

// extractPPTImages parses the raw bytes of the Pictures stream from a PPT OLE2
// container and returns an ImageRef for each embedded image that is ≥ 1KB.
// The Pictures stream contains consecutive BLIP Records. Each record has an
// 8-byte header (recVerInstance, recType, recLen) followed by a variable-length
// BLIP header and then the raw image data.
//
// Supported BLIP types:
//   0xF01A – EMF, 0xF01B – WMF, 0xF01D – JPEG, 0xF01E – PNG
//
// Any individual record that cannot be parsed is silently skipped.
func extractPPTImages(picturesData []byte) []ImageRef {
	var images []ImageRef
	pos := 0
	imageIndex := 1

	for pos+8 <= len(picturesData) {
		// --- Record Header (8 bytes) ---
		recVerInstance := binary.LittleEndian.Uint16(picturesData[pos : pos+2])
		recType := binary.LittleEndian.Uint16(picturesData[pos+2 : pos+4])
		recLen := binary.LittleEndian.Uint32(picturesData[pos+4 : pos+8])

		recInstance := recVerInstance >> 4

		// Sanity check: recLen must not exceed remaining data
		if int(recLen) > len(picturesData)-(pos+8) {
			break
		}

		recordDataStart := pos + 8
		pos += 8 + int(recLen) // advance to next record regardless of outcome

		// Determine BLIP header size based on recType and whether it's dual-UID.
		// Dual-UID is indicated by recInstance having bit 4 set (recInstance & 0x10 != 0).
		var blipHeaderSize int
		switch recType {
		case 0xF01A, 0xF01B: // EMF, WMF
			// Single UID: 16 (UID) + 34 (metafile header) = 50
			// Dual UID:   32 (2×UID) + 34 (metafile header) = 66
			if recInstance&0x10 != 0 {
				blipHeaderSize = 66
			} else {
				blipHeaderSize = 50
			}
		case 0xF01D, 0xF01E: // JPEG, PNG
			// Single UID: 16 (UID) + 1 (tag) = 17
			// Dual UID:   32 (2×UID) + 1 (tag) = 33
			if recInstance&0x10 != 0 {
				blipHeaderSize = 33
			} else {
				blipHeaderSize = 17
			}
		default:
			// Unknown BLIP type – skip this record
			continue
		}

		// Validate that the BLIP header fits within recLen
		if int(recLen) < blipHeaderSize {
			continue
		}

		imageData := append([]byte(nil), picturesData[recordDataStart+blipHeaderSize:recordDataStart+int(recLen)]...)

		// Apply minimum size filter
		if len(imageData) < minImageSize {
			continue
		}

		images = append(images, ImageRef{
			Alt:  fmt.Sprintf("PPT图片%d", imageIndex),
			Data: imageData,
		})
		imageIndex++
	}

	return images
}

// extractDocImages scans the raw bytes of a DOC Data stream for embedded
// JPEG and PNG images using magic-number detection. Images smaller than 1KB
// are filtered out. Each valid image is returned as an ImageRef with Alt
// set to "DOC图片N" (N starting from 1). Extraction failures for individual
// images are silently skipped.
func extractDocImages(dataStream []byte) []ImageRef {
	if len(dataStream) == 0 {
		return nil
	}

	var images []ImageRef
	imageIndex := 1
	pos := 0

	jpegMagic := []byte{0xFF, 0xD8, 0xFF}
	jpegEOI := []byte{0xFF, 0xD9}
	pngMagic := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}
	pngIEND := []byte{0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4E, 0x44, 0xAE, 0x42, 0x60, 0x82}

	for pos < len(dataStream) {
		// Check for JPEG magic
		if pos+3 <= len(dataStream) && bytes.Equal(dataStream[pos:pos+3], jpegMagic) {
			// Find the boundary: next image magic or end of stream
			boundary := len(dataStream)
			for scan := pos + 3; scan < len(dataStream); scan++ {
				if scan+3 <= len(dataStream) && bytes.Equal(dataStream[scan:scan+3], jpegMagic) {
					boundary = scan
					break
				}
				if scan+8 <= len(dataStream) && bytes.Equal(dataStream[scan:scan+8], pngMagic) {
					boundary = scan
					break
				}
			}
			// Find the LAST FF D9 within the boundary
			searchRegion := dataStream[pos+3 : boundary]
			lastEOI := bytes.LastIndex(searchRegion, jpegEOI)
			if lastEOI >= 0 {
				endPos := pos + 3 + lastEOI + 2 // include the EOI marker
				imgData := dataStream[pos:endPos]
				if len(imgData) >= minImageSize {
					images = append(images, ImageRef{
						Alt:  fmt.Sprintf("DOC图片%d", imageIndex),
						Data: append([]byte(nil), imgData...), // copy to avoid holding entire stream
					})
					imageIndex++
				}
				pos = endPos
				continue
			}
			// No EOI found — skip this JPEG start and continue scanning
			pos++
			continue
		}

		// Check for PNG magic
		if pos+8 <= len(dataStream) && bytes.Equal(dataStream[pos:pos+8], pngMagic) {
			iendIdx := bytes.Index(dataStream[pos+8:], pngIEND)
			if iendIdx >= 0 {
				endPos := pos + 8 + iendIdx + len(pngIEND)
				imgData := dataStream[pos:endPos]
				if len(imgData) >= minImageSize {
					images = append(images, ImageRef{
						Alt:  fmt.Sprintf("DOC图片%d", imageIndex),
						Data: append([]byte(nil), imgData...), // copy to avoid holding entire stream
					})
					imageIndex++
				}
				pos = endPos
				continue
			}
			// No IEND found — skip this PNG start and continue scanning
			pos++
			continue
		}

		pos++
	}

	return images
}
