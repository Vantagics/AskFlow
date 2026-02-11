//go:build amd64

package sqlitevec

import "golang.org/x/sys/cpu"

var (
	hasAVX512 = cpu.X86.HasAVX512F
	hasAVX2   = cpu.X86.HasAVX2 && cpu.X86.HasFMA
)

func dotProductSIMD(a, b []float32) float32 {
	n := len(a)
	if n == 0 {
		return 0
	}
	if hasAVX512 && n >= 64 {
		return dotProductAVX512(a, b)
	}
	if hasAVX2 && n >= 32 {
		return dotProductAVX2(a, b)
	}
	if n >= 16 {
		return dotProductSSE(a, b)
	}
	return dotProductF32x8(a, b)
}

func vectorNormSIMD(v []float32) float32 {
	dot := dotProductSIMD(v, v)
	return float32Sqrt(dot)
}

func float32Sqrt(x float32) float32 {
	return float32(sqrt64(float64(x)))
}

//go:nosplit
func sqrt64(x float64) float64 {
	if x <= 0 {
		return 0
	}
	return sqrtAsm(x)
}

// Implemented in simd_amd64.s
func dotProductAVX512(a, b []float32) float32
func dotProductAVX2(a, b []float32) float32
func dotProductSSE(a, b []float32) float32
func sqrtAsm(x float64) float64

func simdCapability() string {
	if hasAVX512 {
		return "AVX-512 + FMA (amd64)"
	}
	if hasAVX2 {
		return "AVX2 + FMA (amd64)"
	}
	return "SSE (amd64)"
}
