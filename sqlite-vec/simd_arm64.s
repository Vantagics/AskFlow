//go:build arm64

#include "textflag.h"

// func dotProductNEON(a, b []float32) float32
TEXT ·dotProductNEON(SB), NOSPLIT, $0-52
    MOVD    a_base+0(FP), R0
    MOVD    a_len+8(FP), R1
    MOVD    b_base+24(FP), R2

    VEOR    V0.B16, V0.B16, V0.B16
    VEOR    V1.B16, V1.B16, V1.B16
    VEOR    V2.B16, V2.B16, V2.B16
    VEOR    V3.B16, V3.B16, V3.B16

    CMP     $16, R1
    BLT     neon_tail8

neon_loop16:
    VLD1.P  16(R0), [V4.S4]
    VLD1.P  16(R2), [V5.S4]
    VFMLA   V4.S4, V5.S4, V0.S4

    VLD1.P  16(R0), [V6.S4]
    VLD1.P  16(R2), [V7.S4]
    VFMLA   V6.S4, V7.S4, V1.S4

    VLD1.P  16(R0), [V4.S4]
    VLD1.P  16(R2), [V5.S4]
    VFMLA   V4.S4, V5.S4, V2.S4

    VLD1.P  16(R0), [V6.S4]
    VLD1.P  16(R2), [V7.S4]
    VFMLA   V6.S4, V7.S4, V3.S4

    SUB     $16, R1, R1
    CMP     $16, R1
    BGE     neon_loop16

    VADD    V1.S4, V0.S4, V0.S4
    VADD    V3.S4, V2.S4, V2.S4
    VADD    V2.S4, V0.S4, V0.S4

neon_tail8:
    CMP     $8, R1
    BLT     neon_tail4

    VLD1.P  16(R0), [V4.S4]
    VLD1.P  16(R2), [V5.S4]
    VFMLA   V4.S4, V5.S4, V0.S4

    VLD1.P  16(R0), [V6.S4]
    VLD1.P  16(R2), [V7.S4]
    VFMLA   V6.S4, V7.S4, V0.S4

    SUB     $8, R1, R1

neon_tail4:
    CMP     $4, R1
    BLT     neon_reduce

    VLD1.P  16(R0), [V4.S4]
    VLD1.P  16(R2), [V5.S4]
    VFMLA   V4.S4, V5.S4, V0.S4

    SUB     $4, R1, R1

neon_reduce:
    VEXT    $8, V0.B16, V0.B16, V1.B16
    VADD    V1.S4, V0.S4, V0.S4
    VEXT    $4, V0.B16, V0.B16, V1.B16
    VADD    V1.S4, V0.S4, V0.S4

    CBZ     R1, neon_done

neon_scalar:
    FMOVS   (R0), F1
    FMOVS   (R2), F2
    FMADDS  F1, F2, F0, F0
    ADD     $4, R0
    ADD     $4, R2
    SUB     $1, R1, R1
    CBNZ    R1, neon_scalar

neon_done:
    FMOVS   F0, ret+48(FP)
    RET
