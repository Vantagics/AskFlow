//go:build amd64

#include "textflag.h"

// func dotProductAVX512(a, b []float32) float32
TEXT ·dotProductAVX512(SB), NOSPLIT, $0-52
    MOVQ    a_base+0(FP), SI
    MOVQ    a_len+8(FP), CX
    MOVQ    b_base+24(FP), DI

    VXORPS  Z0, Z0, Z0
    VXORPS  Z1, Z1, Z1
    VXORPS  Z2, Z2, Z2
    VXORPS  Z3, Z3, Z3

    CMPQ    CX, $64
    JL      avx512_tail32

avx512_loop64:
    VMOVUPS 0(SI), Z4
    VMOVUPS 0(DI), Z5
    VFMADD231PS Z4, Z5, Z0

    VMOVUPS 64(SI), Z6
    VMOVUPS 64(DI), Z7
    VFMADD231PS Z6, Z7, Z1

    VMOVUPS 128(SI), Z4
    VMOVUPS 128(DI), Z5
    VFMADD231PS Z4, Z5, Z2

    VMOVUPS 192(SI), Z6
    VMOVUPS 192(DI), Z7
    VFMADD231PS Z6, Z7, Z3

    ADDQ    $256, SI
    ADDQ    $256, DI
    SUBQ    $64, CX
    CMPQ    CX, $64
    JGE     avx512_loop64

avx512_tail32:
    VADDPS  Z1, Z0, Z0
    VADDPS  Z3, Z2, Z2
    VADDPS  Z2, Z0, Z0

    CMPQ    CX, $32
    JL      avx512_tail16

    VMOVUPS 0(SI), Z4
    VMOVUPS 0(DI), Z5
    VFMADD231PS Z4, Z5, Z0

    VMOVUPS 64(SI), Z6
    VMOVUPS 64(DI), Z7
    VFMADD231PS Z6, Z7, Z0

    ADDQ    $128, SI
    ADDQ    $128, DI
    SUBQ    $32, CX

avx512_tail16:
    CMPQ    CX, $16
    JL      avx512_reduce

    VMOVUPS 0(SI), Z4
    VMOVUPS 0(DI), Z5
    VFMADD231PS Z4, Z5, Z0

    ADDQ    $64, SI
    ADDQ    $64, DI
    SUBQ    $16, CX

avx512_reduce:
    VEXTRACTF64X4 $1, Z0, Y1
    VADDPS  Y1, Y0, Y0
    VEXTRACTF128 $1, Y0, X1
    VADDPS  X1, X0, X0
    VMOVHLPS X0, X1, X1
    VADDPS  X1, X0, X0
    VPSHUFD $0x01, X0, X1
    VADDSS  X1, X0, X0

    CMPQ    CX, $0
    JE      avx512_done

avx512_scalar:
    VMOVSS  0(SI), X1
    VMOVSS  0(DI), X2
    VFMADD231SS X1, X2, X0
    ADDQ    $4, SI
    ADDQ    $4, DI
    DECQ    CX
    JNZ     avx512_scalar

avx512_done:
    VZEROUPPER
    MOVSS   X0, ret+48(FP)
    RET


// func dotProductAVX2(a, b []float32) float32
TEXT ·dotProductAVX2(SB), NOSPLIT, $0-52
    MOVQ    a_base+0(FP), SI
    MOVQ    a_len+8(FP), CX
    MOVQ    b_base+24(FP), DI

    VXORPS  Y0, Y0, Y0
    VXORPS  Y1, Y1, Y1
    VXORPS  Y2, Y2, Y2
    VXORPS  Y3, Y3, Y3

    CMPQ    CX, $32
    JL      avx2_tail16

avx2_loop32:
    VMOVUPS 0(SI), Y4
    VMOVUPS 0(DI), Y5
    VFMADD231PS Y4, Y5, Y0

    VMOVUPS 32(SI), Y6
    VMOVUPS 32(DI), Y7
    VFMADD231PS Y6, Y7, Y1

    VMOVUPS 64(SI), Y4
    VMOVUPS 64(DI), Y5
    VFMADD231PS Y4, Y5, Y2

    VMOVUPS 96(SI), Y6
    VMOVUPS 96(DI), Y7
    VFMADD231PS Y6, Y7, Y3

    ADDQ    $128, SI
    ADDQ    $128, DI
    SUBQ    $32, CX
    CMPQ    CX, $32
    JGE     avx2_loop32

avx2_tail16:
    VADDPS  Y1, Y0, Y0
    VADDPS  Y3, Y2, Y2
    VADDPS  Y2, Y0, Y0

    CMPQ    CX, $16
    JL      avx2_tail8

    VMOVUPS 0(SI), Y4
    VMOVUPS 0(DI), Y5
    VFMADD231PS Y4, Y5, Y0

    VMOVUPS 32(SI), Y6
    VMOVUPS 32(DI), Y7
    VFMADD231PS Y6, Y7, Y0

    ADDQ    $64, SI
    ADDQ    $64, DI
    SUBQ    $16, CX

avx2_tail8:
    CMPQ    CX, $8
    JL      avx2_reduce

    VMOVUPS 0(SI), Y4
    VMOVUPS 0(DI), Y5
    VFMADD231PS Y4, Y5, Y0

    ADDQ    $32, SI
    ADDQ    $32, DI
    SUBQ    $8, CX

avx2_reduce:
    VEXTRACTF128 $1, Y0, X1
    VADDPS  X1, X0, X0
    VMOVHLPS X0, X1, X1
    VADDPS  X1, X0, X0
    VPSHUFD $0x01, X0, X1
    VADDSS  X1, X0, X0

    CMPQ    CX, $0
    JE      avx2_done

avx2_scalar:
    VMOVSS  0(SI), X1
    VMOVSS  0(DI), X2
    VFMADD231SS X1, X2, X0
    ADDQ    $4, SI
    ADDQ    $4, DI
    DECQ    CX
    JNZ     avx2_scalar

avx2_done:
    VZEROUPPER
    MOVSS   X0, ret+48(FP)
    RET


// func dotProductSSE(a, b []float32) float32
TEXT ·dotProductSSE(SB), NOSPLIT, $0-52
    MOVQ    a_base+0(FP), SI
    MOVQ    a_len+8(FP), CX
    MOVQ    b_base+24(FP), DI

    XORPS   X0, X0
    XORPS   X1, X1
    XORPS   X2, X2
    XORPS   X3, X3

    CMPQ    CX, $16
    JL      sse_tail4

sse_loop16:
    MOVUPS  0(SI), X4
    MOVUPS  0(DI), X5
    MULPS   X5, X4
    ADDPS   X4, X0

    MOVUPS  16(SI), X6
    MOVUPS  16(DI), X7
    MULPS   X7, X6
    ADDPS   X6, X1

    MOVUPS  32(SI), X4
    MOVUPS  32(DI), X5
    MULPS   X5, X4
    ADDPS   X4, X2

    MOVUPS  48(SI), X6
    MOVUPS  48(DI), X7
    MULPS   X7, X6
    ADDPS   X6, X3

    ADDQ    $64, SI
    ADDQ    $64, DI
    SUBQ    $16, CX
    CMPQ    CX, $16
    JGE     sse_loop16

    ADDPS   X1, X0
    ADDPS   X3, X2
    ADDPS   X2, X0

sse_tail4:
    CMPQ    CX, $4
    JL      sse_reduce

    MOVUPS  0(SI), X4
    MOVUPS  0(DI), X5
    MULPS   X5, X4
    ADDPS   X4, X0

    ADDQ    $16, SI
    ADDQ    $16, DI
    SUBQ    $4, CX
    JMP     sse_tail4

sse_reduce:
    MOVHLPS X0, X1
    ADDPS   X1, X0
    PSHUFD  $0x01, X0, X1
    ADDSS   X1, X0

    CMPQ    CX, $0
    JE      sse_done

sse_scalar:
    MOVSS   0(SI), X1
    MOVSS   0(DI), X2
    MULSS   X2, X1
    ADDSS   X1, X0
    ADDQ    $4, SI
    ADDQ    $4, DI
    DECQ    CX
    JNZ     sse_scalar

sse_done:
    MOVSS   X0, ret+48(FP)
    RET


// func sqrtAsm(x float64) float64
TEXT ·sqrtAsm(SB), NOSPLIT, $0-16
    MOVSD   x+0(FP), X0
    SQRTSD  X0, X0
    MOVSD   X0, ret+8(FP)
    RET
