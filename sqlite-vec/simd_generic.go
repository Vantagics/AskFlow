//go:build !amd64 && !arm64

package sqlitevec

import "math"

func dotProductSIMD(a, b []float32) float32 {
	return dotProductF32x8(a, b)
}

func vectorNormSIMD(v []float32) float32 {
	var sum float32
	n := len(v)
	i := 0
	for ; i <= n-8; i += 8 {
		sum += v[i]*v[i] + v[i+1]*v[i+1] + v[i+2]*v[i+2] + v[i+3]*v[i+3] +
			v[i+4]*v[i+4] + v[i+5]*v[i+5] + v[i+6]*v[i+6] + v[i+7]*v[i+7]
	}
	for ; i < n; i++ {
		sum += v[i] * v[i]
	}
	return float32(math.Sqrt(float64(sum)))
}

func simdCapability() string {
	return "Go (no SIMD assembly for this platform)"
}
