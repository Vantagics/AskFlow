// Package chunker provides text splitting functionality for document processing.
// It splits text into fixed-size chunks with configurable overlap.
package chunker

// DefaultChunkSize is the default number of characters per chunk.
const DefaultChunkSize = 512

// DefaultOverlap is the default number of overlapping characters between adjacent chunks.
const DefaultOverlap = 128

// TextChunker splits text into fixed-size chunks with configurable overlap.
type TextChunker struct {
	ChunkSize int // default 512
	Overlap   int // default 128
}

// Chunk represents a segment of text from a document.
type Chunk struct {
	Text       string `json:"text"`
	Index      int    `json:"index"`
	DocumentID string `json:"document_id"`
}

// NewTextChunker creates a TextChunker with default settings.
func NewTextChunker() *TextChunker {
	return &TextChunker{
		ChunkSize: DefaultChunkSize,
		Overlap:   DefaultOverlap,
	}
}

// Split divides text into chunks of ChunkSize runes with Overlap runes
// of overlap between adjacent chunks. Each chunk is tagged with the given documentID
// and an incrementing index starting from 0.
//
// Uses rune-based splitting to correctly handle multi-byte Unicode characters.
// Returns an empty slice for empty text.
// Returns a single chunk if text is shorter than or equal to ChunkSize.
// The last chunk may be shorter than ChunkSize.
func (tc *TextChunker) Split(text string, documentID string) []Chunk {
	if len(text) == 0 {
		return []Chunk{}
	}

	runes := []rune(text)

	chunkSize := tc.ChunkSize
	if chunkSize <= 0 {
		chunkSize = DefaultChunkSize
	}

	overlap := tc.Overlap
	if overlap < 0 {
		overlap = 0
	}
	if overlap >= chunkSize {
		overlap = chunkSize - 1
	}

	step := chunkSize - overlap
	var chunks []Chunk
	index := 0

	for start := 0; start < len(runes); start += step {
		end := start + chunkSize
		if end > len(runes) {
			end = len(runes)
		}

		chunks = append(chunks, Chunk{
			Text:       string(runes[start:end]),
			Index:      index,
			DocumentID: documentID,
		})
		index++

		// If we've reached the end of the text, stop
		if end == len(runes) {
			break
		}
	}

	return chunks
}
