// Copyright (c) 2025 Hans-Kristian Arntzen
// Copyright (c) 2026 The Planck Authors
// SPDX-License-Identifier: MIT
//
// PyroWave decoder for Metal, ported from PyroWave's MIT shaders
// (wavelet_dequant.comp, idwt.comp, yuv2rgb.frag; https://github.com/
// Themaister/pyrowave) by way of shaders/hlsl/decode.hlsl, which it
// follows line for line. See LICENSE.pyrowave.

#include <metal_stdlib>
using namespace metal;

constant float ALPHA = -1.586134342059924;
constant float BETA = -0.052980118572961;
constant float GAMMA = 0.882911075530934;
constant float DELTA = 0.443506852043971;
constant float K = 1.230174104914001;
constant float INV_K = 1.0 / 1.230174104914001;
constant float CHROMA_ZERO = 128.0 / 255.0;
constant uint BAND_WORDS = 12;
constant int TILE = 40;

struct Params
{
	int2 imageSize;
	int2 planeSize;
	uint inOff;
	uint chroma420;
	uint totalBlocks;
	uint budgetWords;
	uint4 outOff;
	uint seq;
	uint numBands;
	float kr;
	float kb;
	int2 chromaSize;
	uint groupsX;
	uint tenBit;
};

static int mirror(int i, int n)
{
	if (uint(i) < uint(n))
		return i;
	int p = 2 * n - 2;
	i = abs(i) % p;
	return i >= n ? p - i : i;
}

static int2 unswizzle8x8(uint i)
{
	uint y = (i & 1u) | (((i >> 3) & 3u) << 1);
	uint x = ((i >> 1) & 3u) | (((i >> 5) & 1u) << 2);
	return int2(x, y);
}

static float block8x8Scale(uint code) { return float(code) / 8.0 + 0.25; }

static float block32x32Scale(uint code)
{
	int e = 4 - int(code >> 3);
	float m = float(8u + (code & 7u));
	return m * exp2(float(20 + e)) / (8.0 * 1024.0 * 1024.0);
}

static uint byteAt(device const uint* up, uint base, uint addr)
{
	return (up[(base + addr) >> 2] >> (((base + addr) & 3u) * 8)) & 0xffu;
}

kernel void clear(device uint4* coef [[buffer(0)]], constant Params& P [[buffer(4)]],
	uint3 id [[thread_position_in_grid]])
{
	uint i = id.y * P.groupsX * 256 + id.x;
	if (i * 4 < P.outOff.x)
		coef[i] = uint4(0);
}

static uint scan128(threadgroup uint* s, uint li, uint v)
{
	s[li] = v;
	threadgroup_barrier(mem_flags::mem_threadgroup);
	for (uint step = 1; step < 128; step *= 2)
	{
		uint u = li >= step ? s[li - step] : 0;
		threadgroup_barrier(mem_flags::mem_threadgroup);
		s[li] += u;
		threadgroup_barrier(mem_flags::mem_threadgroup);
	}
	return s[li];
}

kernel void dequant(device float* coef [[buffer(0)]], device const uint* bands [[buffer(1)]],
	device const uint4* recs [[buffer(2)]], device const uint* up [[buffer(3)]],
	constant Params& P [[buffer(4)]],
	uint3 gid [[threadgroup_position_in_grid]], uint li [[thread_index_in_threadgroup]])
{
	threadgroup uint gsScan[128];
	uint r = gid.y * P.groupsX + gid.x;
	if (r >= P.totalBlocks)
		return;
	uint4 rec = recs[r];
	uint base = rec.x * 4, len = rec.y * 4, block = rec.z;
	device const uint* b = bands + rec.w * BAND_WORDS;
	uint bOffset = b[0], bWidth = b[1], bHeight = b[2], bBlocksX = b[3], bFirst = b[4];
	uint w0 = up[rec.x], w1 = up[rec.x + 1];
	uint ballot = w0 & 0xffffu;
	uint n = popcount(ballot);
	uint j = li >> 3, s = li & 7u;
	bool active = ((ballot >> j) & 1u) != 0;
	uint k = popcount(ballot & ((1u << j) - 1u));
	uint planes = 0, qs = 0;
	if (active)
	{
		uint cw = byteAt(up, base, 8 + 2 * k) | (byteAt(up, base, 9 + 2 * k) << 8);
		qs = byteAt(up, base, 8 + 2 * n + k);
		planes = ((cw >> (2 * s)) & 3u) + (qs & 15u);
	}
	uint incl = scan128(gsScan, li, planes);
	uint total = gsScan[127];
	uint addr = 8 + 3 * n + incl - planes;
	uint v[8] = { 0, 0, 0, 0, 0, 0, 0, 0 };
	for (uint pl = 0; pl < planes; pl++, addr++)
	{
		uint by = addr < len ? byteAt(up, base, addr) : 0;
		for (uint p = 0; p < 8; p++)
			v[p] = (v[p] << 1) | ((by >> p) & 1u);
	}
	uint nz = 0;
	for (uint p = 0; p < 8; p++)
		nz += v[p] != 0 ? 1u : 0u;
	threadgroup_barrier(mem_flags::mem_threadgroup);
	uint bit = (8 + 3 * n + total) * 8 + scan128(gsScan, li, nz) - nz;
	if (!active)
		return;
	float scale = block32x32Scale(w1 & 0xffu) * block8x8Scale(qs >> 4);
	uint local = block - bFirst;
	uint bx = local % bBlocksX, by8 = local / bBlocksX;
	for (uint p = 0; p < 8; p++)
	{
		if (v[p] == 0)
			continue;
		uint byteAddr = bit >> 3;
		uint sign = byteAddr < len ? (byteAt(up, base, byteAddr) >> (bit & 7u)) & 1u : 0;
		bit++;
		float c = (float(v[p]) + 0.5) * scale;
		int2 xy = unswizzle8x8(s * 8 + p);
		uint X = bx * 32 + (j & 3u) * 8 + xy.x, Y = by8 * 32 + (j >> 2) * 8 + xy.y;
		if (X < bWidth && Y < bHeight)
			coef[bOffset + Y * bWidth + X] = sign != 0 ? -c : c;
	}
}

static void unliftCols(threadgroup float (*tile)[TILE + 1], uint li, uint parity, float c)
{
	for (uint i = li; i < TILE * 20; i += 256)
	{
		int col = i % TILE;
		int y = 2 * (i / TILE) + parity;
		if (y >= 1 && y <= TILE - 2)
			tile[y][col] -= c * (tile[y - 1][col] + tile[y + 1][col]);
	}
	threadgroup_barrier(mem_flags::mem_threadgroup);
}

static void unliftRows(threadgroup float (*tile)[TILE + 1], uint li, uint parity, float c)
{
	for (uint i = li; i < TILE * 20; i += 256)
	{
		int row = i % TILE;
		int x = 2 * (i / TILE) + parity;
		if (x >= 1 && x <= TILE - 2)
			tile[row][x] -= c * (tile[row][x - 1] + tile[row][x + 1]);
	}
	threadgroup_barrier(mem_flags::mem_threadgroup);
}

// planeSize is the output plane, outOff the four input bands, inOff the
// output plane's offset.
kernel void idwt(device float* coef [[buffer(0)]], constant Params& P [[buffer(4)]],
	uint3 gid [[threadgroup_position_in_grid]], uint li [[thread_index_in_threadgroup]])
{
	threadgroup float tile[TILE][TILE + 1];
	int2 base = int2(gid.xy) * 32 - 4;
	int W = P.planeSize.x, H = P.planeSize.y, hw = W / 2;
	for (uint i = li; i < TILE * TILE; i += 256)
	{
		int ty = i / TILE, tx = i % TILE;
		int x = mirror(base.x + tx, W), y = mirror(base.y + ty, H);
		uint kind = (x & 1) | ((y & 1) << 1);
		float s = ((x & 1) != 0 ? INV_K : K) * ((y & 1) != 0 ? INV_K : K);
		tile[ty][tx] = coef[P.outOff[kind] + (y / 2) * hw + x / 2] * s;
	}
	threadgroup_barrier(mem_flags::mem_threadgroup);
	unliftCols(tile, li, 0, DELTA);
	unliftCols(tile, li, 1, GAMMA);
	unliftCols(tile, li, 0, BETA);
	unliftCols(tile, li, 1, ALPHA);
	unliftRows(tile, li, 0, DELTA);
	unliftRows(tile, li, 1, GAMMA);
	unliftRows(tile, li, 0, BETA);
	unliftRows(tile, li, 1, ALPHA);
	for (uint j = li; j < 32 * 32; j += 256)
	{
		int ty = 4 + j / 32, tx = 4 + j % 32;
		int2 p = base + int2(tx, ty);
		if (p.x < W && p.y < H)
			coef[P.inOff + p.y * W + p.x] = tile[ty][tx];
	}
}

static float chromaAt(device const float* coef, constant Params& P, uint off, float2 f)
{
	int2 p0 = int2(floor(f));
	float2 t = f - float2(p0);
	int2 last = P.imageSize / 2 - 1;
	int2 p1 = clamp(p0 + 1, int2(0), last);
	p0 = clamp(p0, int2(0), last);
	int stride = P.chromaSize.x;
	float a = saturate(coef[off + p0.y * stride + p0.x] + 0.5);
	float b = saturate(coef[off + p0.y * stride + p1.x] + 0.5);
	float c = saturate(coef[off + p1.y * stride + p0.x] + 0.5);
	float d = saturate(coef[off + p1.y * stride + p1.x] + 0.5);
	return mix(mix(a, b, t.x), mix(c, d, t.x), t.y);
}

kernel void yuv2rgb(device const float* coef [[buffer(0)]], constant Params& P [[buffer(4)]],
	texture2d<float, access::write> out [[texture(0)]], uint3 id [[thread_position_in_grid]])
{
	int2 p = int2(id.xy);
	if (any(p >= P.imageSize))
		return;
	float y = saturate(coef[P.outOff.x + p.y * P.planeSize.x + p.x] + 0.5);
	float cb, cr;
	if (P.chroma420 != 0)
	{
		float2 f = float2(p) * 0.5 - 0.25;
		cb = chromaAt(coef, P, P.outOff.y, f);
		cr = chromaAt(coef, P, P.outOff.z, f);
	}
	else
	{
		cb = saturate(coef[P.outOff.y + p.y * P.planeSize.x + p.x] + 0.5);
		cr = saturate(coef[P.outOff.z + p.y * P.planeSize.x + p.x] + 0.5);
	}
	cb -= CHROMA_ZERO;
	cr -= CHROMA_ZERO;
	float kr = P.kr, kb = P.kb, kg = 1.0 - kr - kb;
	float3 rgb = float3(y + (2.0 - 2.0 * kr) * cr,
		y - (kb * (2.0 - 2.0 * kb) / kg) * cb - (kr * (2.0 - 2.0 * kr) / kg) * cr,
		y + (2.0 - 2.0 * kb) * cb);
	out.write(float4(saturate(rgb), 1.0), uint2(p));
}
