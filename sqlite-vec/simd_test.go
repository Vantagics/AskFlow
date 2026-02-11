package sqlitevec

import (
	"math"
	"math/rand"
	"testing"
)

func TestDotProductSIMDCorrectness(t *testing.T) {
	sizes := []int{0, 1, 3, 7, 8, 15, 16, 31, 32, 63, 64, 127, 128, 255, 256, 384, 512, 768, 1024, 1536, 2048, 3072, 4096}
	rng := rand.New(rand.NewSource(42))

	for _, n := range sizes {
		a := make([]float32, n)
		b := make([]float32, n)
		for i := range a {
			a[i] = rng.Float32()*2 - 1
			b[i] = rng.Float32()*2 - 1
		}

		expected := dotProductF32x8(a, b)
		got := dotProductSIMD(a, b)

		diff := math.Abs(float64(expected - got))
		relTol := math.Abs(float64(expected)) * 1e-4
		if relTol < 1e-5 {
			relTol = 1e-5
		}
		if diff > relTol {
			t.Errorf("size=%d: dotProductSIMD=%v, dotProductF32x8=%v, diff=%v", n, got, expected, diff)
		}
	}
}

func TestVectorNormSIMDCorrectness(t *testing.T) {
	sizes := []int{0, 1, 8, 16, 32, 128, 384, 768, 1536}
	rng := rand.New(rand.NewSource(99))

	for _, n := range sizes {
		v := make([]float32, n)
		for i := range v {
			v[i] = rng.Float32()*2 - 1
		}

		expected := vectorNormF32(v)
		got := vectorNormSIMD(v)

		diff := math.Abs(float64(expected - got))
		relTol := math.Abs(float64(expected)) * 1e-5
		if relTol < 1e-6 {
			relTol = 1e-6
		}
		if diff > relTol {
			t.Errorf("size=%d: vectorNormSIMD=%v, vectorNormF32=%v, diff=%v", n, got, expected, diff)
		}
	}
}

func TestDotProductSIMDZeroVectors(t *testing.T) {
	a := make([]float32, 1536)
	b := make([]float32, 1536)
	got := dotProductSIMD(a, b)
	if got != 0 {
		t.Errorf("expected 0 for zero vectors, got %v", got)
	}
}

func TestDotProductSIMDIdentical(t *testing.T) {
	n := 1536
	a := make([]float32, n)
	for i := range a {
		a[i] = 1.0 / float32(math.Sqrt(float64(n)))
	}
	got := dotProductSIMD(a, a)
	if math.Abs(float64(got)-1.0) > 1e-4 {
		t.Errorf("expected ~1.0 for unit vector self-dot, got %v", got)
	}
}

func makeBenchVectors(n int) ([]float32, []float32) {
	rng := rand.New(rand.NewSource(12345))
	a := make([]float32, n)
	b := make([]float32, n)
	for i := range a {
		a[i] = rng.Float32()*2 - 1
		b[i] = rng.Float32()*2 - 1
	}
	return a, b
}

func BenchmarkDotProductPureGo_1536(b *testing.B) {
	a, bv := makeBenchVectors(1536)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		dotProductF32x8(a, bv)
	}
}

func BenchmarkDotProductSIMD_1536(b *testing.B) {
	a, bv := makeBenchVectors(1536)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		dotProductSIMD(a, bv)
	}
}

func BenchmarkDotProductSIMD_768(b *testing.B) {
	a, bv := makeBenchVectors(768)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		dotProductSIMD(a, bv)
	}
}

func BenchmarkDotProductSIMD_3072(b *testing.B) {
	a, bv := makeBenchVectors(3072)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		dotProductSIMD(a, bv)
	}
}

func BenchmarkNormSIMD_1536(b *testing.B) {
	v := make([]float32, 1536)
	rng := rand.New(rand.NewSource(42))
	for i := range v {
		v[i] = rng.Float32()*2 - 1
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		vectorNormSIMD(v)
	}
}
