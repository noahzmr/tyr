#include "textflag.h"

// One FFT stage block: for k < n,
//   t = w[k]·v[k];  u[k], v[k] = u[k] + t, u[k] − t   (complex, split arrays)
// tr = vr·wr − vi·wi and ti = vr·wi + vi·wr with fused multiply-add.

// func ditAVX512(ur, ui, vr, vi, wr, wi *float64, n int)
TEXT ·ditAVX512(SB), NOSPLIT, $0-56
	MOVQ ur+0(FP), AX
	MOVQ ui+8(FP), BX
	MOVQ vr+16(FP), CX
	MOVQ vi+24(FP), DX
	MOVQ wr+32(FP), SI
	MOVQ wi+40(FP), DI
	MOVQ n+48(FP), R8
	SHLQ $3, R8
	XORQ R9, R9

dit512:
	CMPQ R9, R8
	JGE  ditdone512
	VMOVUPD (CX)(R9*1), Z0       // vr
	VMOVUPD (DX)(R9*1), Z1       // vi
	VMOVUPD (SI)(R9*1), Z2       // wr
	VMOVUPD (DI)(R9*1), Z3       // wi
	VMULPD  Z3, Z1, Z4           // vi·wi
	VFMSUB231PD Z2, Z0, Z4       // tr = vr·wr − vi·wi
	VMULPD  Z2, Z1, Z5           // vi·wr
	VFMADD231PD Z3, Z0, Z5       // ti = vr·wi + vi·wr
	VMOVUPD (AX)(R9*1), Z6       // ur
	VMOVUPD (BX)(R9*1), Z7       // ui
	VADDPD  Z4, Z6, Z8
	VSUBPD  Z4, Z6, Z9
	VADDPD  Z5, Z7, Z10
	VSUBPD  Z5, Z7, Z11
	VMOVUPD Z8, (AX)(R9*1)
	VMOVUPD Z10, (BX)(R9*1)
	VMOVUPD Z9, (CX)(R9*1)
	VMOVUPD Z11, (DX)(R9*1)
	ADDQ $64, R9
	JMP  dit512

ditdone512:
	VZEROUPPER
	RET

// func ditAVX2(ur, ui, vr, vi, wr, wi *float64, n int)
TEXT ·ditAVX2(SB), NOSPLIT, $0-56
	MOVQ ur+0(FP), AX
	MOVQ ui+8(FP), BX
	MOVQ vr+16(FP), CX
	MOVQ vi+24(FP), DX
	MOVQ wr+32(FP), SI
	MOVQ wi+40(FP), DI
	MOVQ n+48(FP), R8
	SHLQ $3, R8
	XORQ R9, R9

dit256:
	CMPQ R9, R8
	JGE  ditdone256
	VMOVUPD (CX)(R9*1), Y0
	VMOVUPD (DX)(R9*1), Y1
	VMOVUPD (SI)(R9*1), Y2
	VMOVUPD (DI)(R9*1), Y3
	VMULPD  Y3, Y1, Y4
	VFMSUB231PD Y2, Y0, Y4
	VMULPD  Y2, Y1, Y5
	VFMADD231PD Y3, Y0, Y5
	VMOVUPD (AX)(R9*1), Y6
	VMOVUPD (BX)(R9*1), Y7
	VADDPD  Y4, Y6, Y8
	VSUBPD  Y4, Y6, Y9
	VADDPD  Y5, Y7, Y10
	VSUBPD  Y5, Y7, Y11
	VMOVUPD Y8, (AX)(R9*1)
	VMOVUPD Y10, (BX)(R9*1)
	VMOVUPD Y9, (CX)(R9*1)
	VMOVUPD Y11, (DX)(R9*1)
	ADDQ $32, R9
	JMP  dit256

ditdone256:
	VZEROUPPER
	RET

// DIF: d = u − v;  u[k] = u[k] + v[k];  v[k] = d·w[k]

// func difAVX512(ur, ui, vr, vi, wr, wi *float64, n int)
TEXT ·difAVX512(SB), NOSPLIT, $0-56
	MOVQ ur+0(FP), AX
	MOVQ ui+8(FP), BX
	MOVQ vr+16(FP), CX
	MOVQ vi+24(FP), DX
	MOVQ wr+32(FP), SI
	MOVQ wi+40(FP), DI
	MOVQ n+48(FP), R8
	SHLQ $3, R8
	XORQ R9, R9

dif512:
	CMPQ R9, R8
	JGE  difdone512
	VMOVUPD (AX)(R9*1), Z0       // ur
	VMOVUPD (BX)(R9*1), Z1       // ui
	VMOVUPD (CX)(R9*1), Z2       // vr
	VMOVUPD (DX)(R9*1), Z3       // vi
	VMOVUPD (SI)(R9*1), Z4       // wr
	VMOVUPD (DI)(R9*1), Z5       // wi
	VADDPD  Z2, Z0, Z6           // ur + vr
	VADDPD  Z3, Z1, Z7           // ui + vi
	VSUBPD  Z2, Z0, Z8           // dr = ur − vr
	VSUBPD  Z3, Z1, Z9           // di = ui − vi
	VMULPD  Z5, Z9, Z10          // di·wi
	VFMSUB231PD Z4, Z8, Z10      // dr·wr − di·wi
	VMULPD  Z4, Z9, Z11          // di·wr
	VFMADD231PD Z5, Z8, Z11      // dr·wi + di·wr
	VMOVUPD Z6, (AX)(R9*1)
	VMOVUPD Z7, (BX)(R9*1)
	VMOVUPD Z10, (CX)(R9*1)
	VMOVUPD Z11, (DX)(R9*1)
	ADDQ $64, R9
	JMP  dif512

difdone512:
	VZEROUPPER
	RET

// func difAVX2(ur, ui, vr, vi, wr, wi *float64, n int)
TEXT ·difAVX2(SB), NOSPLIT, $0-56
	MOVQ ur+0(FP), AX
	MOVQ ui+8(FP), BX
	MOVQ vr+16(FP), CX
	MOVQ vi+24(FP), DX
	MOVQ wr+32(FP), SI
	MOVQ wi+40(FP), DI
	MOVQ n+48(FP), R8
	SHLQ $3, R8
	XORQ R9, R9

dif256:
	CMPQ R9, R8
	JGE  difdone256
	VMOVUPD (AX)(R9*1), Y0
	VMOVUPD (BX)(R9*1), Y1
	VMOVUPD (CX)(R9*1), Y2
	VMOVUPD (DX)(R9*1), Y3
	VMOVUPD (SI)(R9*1), Y4
	VMOVUPD (DI)(R9*1), Y5
	VADDPD  Y2, Y0, Y6
	VADDPD  Y3, Y1, Y7
	VSUBPD  Y2, Y0, Y8
	VSUBPD  Y3, Y1, Y9
	VMULPD  Y5, Y9, Y10
	VFMSUB231PD Y4, Y8, Y10
	VMULPD  Y4, Y9, Y11
	VFMADD231PD Y5, Y8, Y11
	VMOVUPD Y6, (AX)(R9*1)
	VMOVUPD Y7, (BX)(R9*1)
	VMOVUPD Y10, (CX)(R9*1)
	VMOVUPD Y11, (DX)(R9*1)
	ADDQ $32, R9
	JMP  dif256

difdone256:
	VZEROUPPER
	RET
