package vectorstore

import (
	"encoding/binary"
	"math"
)

// SerializeVector converts a float64 slice to a compact byte slice.
// It stores each float64 as a float32 (4 bytes, little-endian) to halve storage size.
// Embedding vectors have sufficient precision at float32.
func SerializeVector(vec []float64) []byte {
	buf := make([]byte, len(vec)*4)
	for i, v := range vec {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(float32(v)))
	}
	return buf
}

// DeserializeVector converts a byte slice back to a float64 slice.
// Supports both legacy float64 format (8 bytes/element) and compact float32 format (4 bytes/element).
func DeserializeVector(data []byte) []float64 {
	if len(data) == 0 {
		return nil
	}
	// Auto-detect format: if length is divisible by 8 but the float64 values look
	// like valid embeddings (all in [-2, 2] range), treat as legacy float64.
	// Otherwise use float32 format.
	if len(data)%4 != 0 {
		return nil
	}
	if len(data)%8 == 0 {
		// Could be either format.
		n64 := len(data) / 8
		n32 := len(data) / 4
		// Heuristic: typical embedding dimensions are 128, 256, 384, 512, 768, 1024, 1536, 2048, 3072, 4096.
		// If n64 matches a common dimension and n32 doesn't, prefer float64 (legacy).
		// If both match common dims, check the actual float64 values to decide.
		// Otherwise prefer float32 (new format) since SerializeVector now produces float32.
		if isCommonDim(n64) && !isCommonDim(n32) {
			return deserializeFloat64(data, n64)
		}
		if isCommonDim(n64) && isCommonDim(n32) {
			// Both are valid dims — inspect values to decide format.
			// Legacy float64 embeddings have values typically in [-2, 2].
			// If interpreted as float64 and values look valid, use float64.
			if looksLikeFloat64Embedding(data, n64) {
				return deserializeFloat64(data, n64)
			}
		}
		return deserializeFloat32(data, n32)
	}
	// Length divisible by 4 but not 8 — must be float32
	n := len(data) / 4
	return deserializeFloat32(data, n)
}

// isCommonDim returns true if n is a common embedding dimension.
func isCommonDim(n int) bool {
	switch n {
	case 128, 256, 384, 512, 768, 1024, 1536, 2048, 3072, 4096:
		return true
	}
	return false
}

// looksLikeFloat64Embedding checks if the first few float64 values are in a valid embedding range.
// Real embedding values are typically in [-2, 2] with most absolute values > 0.001.
// Float32 data reinterpreted as float64 produces values around 1e-5 which are too small
// for real embeddings.
func looksLikeFloat64Embedding(data []byte, n int) bool {
	check := n
	if check > 16 {
		check = 16
	}
	validCount := 0
	for i := 0; i < check; i++ {
		bits := binary.LittleEndian.Uint64(data[i*8:])
		v := math.Float64frombits(bits)
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return false
		}
		absV := math.Abs(v)
		if absV > 10 {
			return false
		}
		// Real embedding values are typically > 0.001 in absolute value.
		// Float32 bytes misread as float64 produce values around 1e-5 or smaller.
		if absV > 0.001 && absV < 5 {
			validCount++
		}
	}
	// At least half the checked values should look like real embedding values
	return validCount >= check/2
}

func deserializeFloat64(data []byte, n int) []float64 {
	vec := make([]float64, n)
	for i := 0; i < n; i++ {
		vec[i] = math.Float64frombits(binary.LittleEndian.Uint64(data[i*8:]))
	}
	return vec
}

func deserializeFloat32(data []byte, n int) []float64 {
	vec := make([]float64, n)
	for i := 0; i < n; i++ {
		vec[i] = float64(math.Float32frombits(binary.LittleEndian.Uint32(data[i*4:])))
	}
	return vec
}

// DeserializeVectorF32 converts a byte slice directly to a float32 slice,
// avoiding the intermediate float64 conversion. This is used by the in-memory
// cache to halve memory usage.
func DeserializeVectorF32(data []byte) []float32 {
	if len(data) == 0 {
		return nil
	}
	if len(data)%4 != 0 {
		return nil
	}
	// Use the same format detection logic as DeserializeVector
	if len(data)%8 == 0 {
		n64 := len(data) / 8
		n32 := len(data) / 4
		if isCommonDim(n64) && !isCommonDim(n32) {
			return deserializeFloat64AsF32(data, n64)
		}
		if isCommonDim(n64) && isCommonDim(n32) {
			if looksLikeFloat64Embedding(data, n64) {
				return deserializeFloat64AsF32(data, n64)
			}
		}
		return deserializeFloat32Direct(data, n32)
	}
	n := len(data) / 4
	return deserializeFloat32Direct(data, n)
}

// deserializeFloat64AsF32 reads float64 data and converts to float32.
func deserializeFloat64AsF32(data []byte, n int) []float32 {
	vec := make([]float32, n)
	for i := 0; i < n; i++ {
		vec[i] = float32(math.Float64frombits(binary.LittleEndian.Uint64(data[i*8:])))
	}
	return vec
}

// deserializeFloat32Direct reads float32 data directly into a float32 slice.
func deserializeFloat32Direct(data []byte, n int) []float32 {
	vec := make([]float32, n)
	for i := 0; i < n; i++ {
		vec[i] = math.Float32frombits(binary.LittleEndian.Uint32(data[i*4:]))
	}
	return vec
}

// CosineSimilarity computes the cosine similarity between two float64 vectors.
// Returns 0 if either vector has zero magnitude.
func CosineSimilarity(a, b []float64) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dotProduct, normA, normB float64
	for i := range a {
		dotProduct += a[i] * b[i]
		normA += a[i] * a[i]
		normB += b[i] * b[i]
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dotProduct / (math.Sqrt(normA) * math.Sqrt(normB))
}
