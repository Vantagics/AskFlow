package sqlitevec

import (
	"encoding/binary"
	"math"
	"testing"
)

func TestSerializeDeserializeRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		vec  []float64
		tol  float64
	}{
		{"empty vector", []float64{}, 0},
		{"single element", []float64{3.14}, 1e-6},
		{"multiple elements", []float64{1.0, 2.0, 3.0, 4.0, 5.0}, 1e-6},
		{"negative values", []float64{-1.5, -2.5, 0.0, 2.5, 1.5}, 1e-6},
		{"typical embedding values", []float64{0.0123, -0.0456, 0.789, -0.321, 0.654}, 1e-6},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := SerializeVector(tt.vec)
			got := DeserializeVector(data)

			if len(got) != len(tt.vec) {
				t.Fatalf("length mismatch: got %d, want %d", len(got), len(tt.vec))
			}
			for i := range tt.vec {
				if math.Abs(got[i]-tt.vec[i]) > tt.tol {
					t.Errorf("index %d: got %v, want %v (tol=%v)", i, got[i], tt.vec[i], tt.tol)
				}
			}
		})
	}
}

func TestDeserializeLegacyFloat64Format(t *testing.T) {
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

func TestCosineSimilarity(t *testing.T) {
	a := []float64{1.0, 2.0, 3.0}
	sim := CosineSimilarity(a, a)
	if math.Abs(sim-1.0) > 1e-10 {
		t.Errorf("identical vectors should have similarity 1.0, got %v", sim)
	}

	b := []float64{1.0, 0.0}
	c := []float64{0.0, 1.0}
	sim = CosineSimilarity(b, c)
	if math.Abs(sim) > 1e-10 {
		t.Errorf("orthogonal vectors should have similarity 0.0, got %v", sim)
	}
}
