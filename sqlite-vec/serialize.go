package sqlitevec

import (
	"encoding/binary"
	"math"
)

// SerializeVector converts a float64 slice to a compact byte slice.
// It stores each float64 as a float32 (4 bytes, little-endian) to halve storage size.
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
	if len(data)%4 != 0 {
		return nil
	}
	if len(data)%8 == 0 {
		n64 := len(data) / 8
		n32 := len(data) / 4
		if isCommonDim(n64) && !isCommonDim(n32) {
			return deserializeFloat64(data, n64)
		}
		if isCommonDim(n64) && isCommonDim(n32) {
			if looksLikeFloat64Embedding(data, n64) {
				return deserializeFloat64(data, n64)
			}
		}
		return deserializeFloat32(data, n32)
	}
	n := len(data) / 4
	return deserializeFloat32(data, n)
}

func isCommonDim(n int) bool {
	switch n {
	case 128, 256, 384, 512, 768, 1024, 1536, 2048, 3072, 4096:
		return true
	}
	return false
}

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
		if absV > 0.001 && absV < 5 {
			validCount++
		}
	}
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

// DeserializeVectorF32 converts a byte slice directly to a float32 slice.
func DeserializeVectorF32(data []byte) []float32 {
	if len(data) == 0 {
		return nil
	}
	if len(data)%4 != 0 {
		return nil
	}
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

func deserializeFloat64AsF32(data []byte, n int) []float32 {
	vec := make([]float32, n)
	for i := 0; i < n; i++ {
		vec[i] = float32(math.Float64frombits(binary.LittleEndian.Uint64(data[i*8:])))
	}
	return vec
}

func deserializeFloat32Direct(data []byte, n int) []float32 {
	vec := make([]float32, n)
	for i := 0; i < n; i++ {
		vec[i] = math.Float32frombits(binary.LittleEndian.Uint32(data[i*4:]))
	}
	return vec
}

// CosineSimilarity computes the cosine similarity between two float64 vectors.
// Uses a two-pass approach to avoid underflow with very small values.
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
	// Use combined sqrt to reduce floating point error
	return dotProduct / math.Sqrt(normA*normB)
}
