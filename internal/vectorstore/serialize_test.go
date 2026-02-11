package vectorstore

import (
	"encoding/binary"
	"math"
	"testing"
)

func TestSerializeDeserializeRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		vec  []float64
		tol  float64 // tolerance for float32 precision loss
	}{
		{"empty vector", []float64{}, 0},
		{"single element", []float64{3.14}, 1e-6},
		{"multiple elements", []float64{1.0, 2.0, 3.0, 4.0, 5.0}, 1e-6},
		{"negative values", []float64{-1.5, -2.5, 0.0, 2.5, 1.5}, 1e-6},
		{"typical embedding values", []float64{0.0123, -0.0456, 0.789, -0.321, 0.654}, 1e-6},
		{"special values", []float64{0.0, math.Inf(1), math.Inf(-1)}, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := SerializeVector(tt.vec)
			got := DeserializeVector(data)

			if len(got) != len(tt.vec) {
				t.Fatalf("length mismatch: got %d, want %d", len(got), len(tt.vec))
			}
			for i := range tt.vec {
				if math.IsNaN(tt.vec[i]) {
					if !math.IsNaN(got[i]) {
						t.Errorf("index %d: expected NaN, got %v", i, got[i])
					}
				} else if math.IsInf(tt.vec[i], 0) {
					if got[i] != tt.vec[i] {
						t.Errorf("index %d: got %v, want %v", i, got[i], tt.vec[i])
					}
				} else if math.Abs(got[i]-tt.vec[i]) > tt.tol {
					t.Errorf("index %d: got %v, want %v (tol=%v)", i, got[i], tt.vec[i], tt.tol)
				}
			}
		})
	}
}

func TestSerializeVectorByteLength(t *testing.T) {
	vec := []float64{1.0, 2.0, 3.0}
	data := SerializeVector(vec)
	// float32 format: 4 bytes per element
	expected := len(vec) * 4
	if len(data) != expected {
		t.Errorf("byte length: got %d, want %d", len(data), expected)
	}
}

func TestDeserializeEmptyData(t *testing.T) {
	got := DeserializeVector([]byte{})
	if len(got) != 0 {
		t.Errorf("expected empty slice, got length %d", len(got))
	}
}

func TestDeserializeLegacyFloat64Format(t *testing.T) {
	// Simulate legacy float64 serialization (8 bytes per element)
	// with a common embedding dimension (384)
	vec := make([]float64, 384)
	for i := range vec {
		vec[i] = float64(i) * 0.001
	}
	data := make([]byte, len(vec)*8)
	for i, v := range vec {
		binary.LittleEndian.PutUint64(data[i*8:], math.Float64bits(v))
	}

	got := DeserializeVector(data)
	if len(got) != 384 {
		t.Fatalf("expected 384 elements, got %d", len(got))
	}
	for i := range vec {
		if math.Abs(got[i]-vec[i]) > 1e-10 {
			t.Errorf("index %d: got %v, want %v", i, got[i], vec[i])
			break
		}
	}
}

func TestDeserializeFloat32Format(t *testing.T) {
	// Serialize with new format and verify round-trip
	vec := make([]float64, 768)
	for i := range vec {
		vec[i] = float64(i)*0.001 - 0.5
	}
	data := SerializeVector(vec)
	got := DeserializeVector(data)

	if len(got) != 768 {
		t.Fatalf("expected 768 elements, got %d", len(got))
	}
	for i := range vec {
		if math.Abs(got[i]-vec[i]) > 1e-5 {
			t.Errorf("index %d: got %v, want %v", i, got[i], vec[i])
			break
		}
	}
}

func TestCosineSimilarityIdenticalVectors(t *testing.T) {
	a := []float64{1.0, 2.0, 3.0}
	sim := CosineSimilarity(a, a)
	if math.Abs(sim-1.0) > 1e-10 {
		t.Errorf("identical vectors should have similarity 1.0, got %v", sim)
	}
}

func TestCosineSimilarityOrthogonalVectors(t *testing.T) {
	a := []float64{1.0, 0.0}
	b := []float64{0.0, 1.0}
	sim := CosineSimilarity(a, b)
	if math.Abs(sim) > 1e-10 {
		t.Errorf("orthogonal vectors should have similarity 0.0, got %v", sim)
	}
}

func TestCosineSimilarityOppositeVectors(t *testing.T) {
	a := []float64{1.0, 2.0, 3.0}
	b := []float64{-1.0, -2.0, -3.0}
	sim := CosineSimilarity(a, b)
	if math.Abs(sim-(-1.0)) > 1e-10 {
		t.Errorf("opposite vectors should have similarity -1.0, got %v", sim)
	}
}

func TestCosineSimilarityZeroVector(t *testing.T) {
	a := []float64{0.0, 0.0, 0.0}
	b := []float64{1.0, 2.0, 3.0}

	if sim := CosineSimilarity(a, b); sim != 0 {
		t.Errorf("zero vector a: expected 0, got %v", sim)
	}
	if sim := CosineSimilarity(b, a); sim != 0 {
		t.Errorf("zero vector b: expected 0, got %v", sim)
	}
	if sim := CosineSimilarity(a, a); sim != 0 {
		t.Errorf("both zero: expected 0, got %v", sim)
	}
}

func TestCosineSimilarityScaledVectors(t *testing.T) {
	a := []float64{1.0, 2.0, 3.0}
	b := []float64{2.0, 4.0, 6.0}
	sim := CosineSimilarity(a, b)
	if math.Abs(sim-1.0) > 1e-10 {
		t.Errorf("scaled vectors should have similarity 1.0, got %v", sim)
	}
}

func TestCosineSimilarityDifferentLengths(t *testing.T) {
	a := []float64{1.0, 2.0}
	b := []float64{1.0, 2.0, 3.0}
	sim := CosineSimilarity(a, b)
	if sim != 0 {
		t.Errorf("different length vectors should return 0, got %v", sim)
	}
}
