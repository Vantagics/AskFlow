package vectorstore

import sqlitevec "github.com/nicexipi/sqlite-vec"

// SerializeVector converts a float64 slice to a compact byte slice.
func SerializeVector(vec []float64) []byte {
	return sqlitevec.SerializeVector(vec)
}

// DeserializeVector converts a byte slice back to a float64 slice.
func DeserializeVector(data []byte) []float64 {
	return sqlitevec.DeserializeVector(data)
}

// DeserializeVectorF32 converts a byte slice directly to a float32 slice.
func DeserializeVectorF32(data []byte) []float32 {
	return sqlitevec.DeserializeVectorF32(data)
}

// DeserializeVectorF32Unsafe performs zero-copy deserialization for float32 format data.
func DeserializeVectorF32Unsafe(data []byte) []float32 {
	return sqlitevec.DeserializeVectorF32Unsafe(data)
}

// CosineSimilarity computes the cosine similarity between two float64 vectors.
func CosineSimilarity(a, b []float64) float64 {
	return sqlitevec.CosineSimilarity(a, b)
}
