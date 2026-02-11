//go:build arm64

package sqlitevec

import "math"

func dotProductSIMD(a, b []float32) float32 {
	n := len(a)
	if n == 0 {
		return 0
	}
	if n >= 16 {
		return dotProductNEON(a, b)
	}
	return dotProductF32x8(a, b)
}

func vectorNormSIMD(v []float32) float32 {
	dot := dotProductSIMD(v, v)
	return float32(math.Sqrt(float64(dot)))
}

// Implemented in simd_arm64.s
func dotProductNEON(a, b []float32) float32

func simdCapability() string {
	return "NEON + FMLA (arm64)"
}
