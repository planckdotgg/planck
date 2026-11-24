// Copyright (c) 2025 Hans-Kristian Arntzen
// Copyright (c) 2026 The Planck Authors
// SPDX-License-Identifier: MIT
//
// PyroWave decoder for Direct3D 11 (cs_5_0), ported from PyroWave's MIT
// shaders (wavelet_dequant.comp, idwt.comp, yuv2rgb.frag). Subgroup
// operations are replaced by groupshared scans. See LICENSE.pyrowave.
// Concatenated after common.hlsl.

RWByteAddressBuffer coef : register(u0);
RWByteAddressBuffer bands : register(u1);
RWByteAddressBuffer recs : register(u2);
RWByteAddressBuffer up : register(u3);
RWTexture2D<unorm float4> outTex : register(u4);

void storeCoef(uint i, float v) { coef.Store(i * 4, asuint(v)); }
float loadCoef(uint i) { return asfloat(coef.Load(i * 4)); }

// clear zeroes the coded bands: outOff.x floats from 0.
[numthreads(256, 1, 1)]
void clear(uint3 id : SV_DispatchThreadID)
{
	uint i = (id.y * groupsX * 256 + id.x) * 4;
	if (i < outOff.x)
		coef.Store4(i * 4, uint4(0, 0, 0, 0));
}

// ---- Dequantization: one group per record, one thread per subblock. ----

groupshared uint gsScan[128];

uint scan128(uint li, uint v)
{
	gsScan[li] = v;
	GroupMemoryBarrierWithGroupSync();
	for (uint step = 1; step < 128; step *= 2)
	{
		uint u = li >= step ? gsScan[li - step] : 0;
		GroupMemoryBarrierWithGroupSync();
		gsScan[li] += u;
		GroupMemoryBarrierWithGroupSync();
	}
	return gsScan[li];
}

[numthreads(128, 1, 1)]
void dequant(uint3 gid : SV_GroupID, uint li : SV_GroupIndex)
{
	uint r = groupBlock(gid);
	if (r >= totalBlocks)
		return;
	uint4 rec = recs.Load4(r * 16);
	uint base = rec.x * 4, len = rec.y * 4, block = rec.z;
	BandDesc b = loadBand(bands, rec.w);
	uint w0 = up.Load(base), w1 = up.Load(base + 4);
	uint ballot = w0 & 0xffffu;
	uint n = countbits(ballot);
	uint j = li >> 3, s = li & 7u;
	bool active = (ballot >> j & 1u) != 0;
	uint k = countbits(ballot & ((1u << j) - 1u));
	uint planes = 0, qs = 0;
	if (active)
	{
		uint cwAddr = 8 + 2 * k, qsAddr = 8 + 2 * n + k;
		uint cw = (up.Load(base + (cwAddr & ~3u)) >> ((cwAddr & 3u) * 8)) & 0xffffu;
		qs = (up.Load(base + (qsAddr & ~3u)) >> ((qsAddr & 3u) * 8)) & 0xffu;
		planes = ((cw >> (2 * s)) & 3u) + (qs & 15u);
	}
	uint incl = scan128(li, planes);
	uint P = gsScan[127];
	uint addr = 8 + 3 * n + incl - planes;
	uint v[8] = { 0, 0, 0, 0, 0, 0, 0, 0 };
	for (uint pl = 0; pl < planes; pl++, addr++)
	{
		uint by = addr < len ? (up.Load(base + (addr & ~3u)) >> ((addr & 3u) * 8)) & 0xffu : 0;
		[unroll]
		for (uint p = 0; p < 8; p++)
			v[p] = (v[p] << 1) | ((by >> p) & 1u);
	}
	uint nz = 0;
	[unroll]
	for (uint p2 = 0; p2 < 8; p2++)
		nz += v[p2] != 0 ? 1u : 0u;
	GroupMemoryBarrierWithGroupSync();
	uint bit = (8 + 3 * n + P) * 8 + scan128(li, nz) - nz;
	if (!active)
		return;
	float scale = block32x32Scale(w1 & 0xffu) * block8x8Scale(qs >> 4);
	uint local = block - b.firstBlock;
	uint bx = local % b.blocksX, by8 = local / b.blocksX;
	[unroll]
	for (uint p3 = 0; p3 < 8; p3++)
	{
		if (v[p3] == 0)
			continue;
		uint byteAddr = bit >> 3;
		uint sign = byteAddr < len ? (up.Load(base + (byteAddr & ~3u)) >> ((byteAddr & 3u) * 8 + (bit & 7u))) & 1u : 0;
		bit++;
		float c = (float(v[p3]) + 0.5) * scale;
		int2 xy = unswizzle8x8(s * 8 + p3);
		uint X = bx * 32 + (j & 3u) * 8 + xy.x, Y = by8 * 32 + (j >> 2) * 8 + xy.y;
		if (X < b.width && Y < b.height)
			storeCoef(b.offset + Y * b.width + X, sign != 0 ? -c : c);
	}
}

// ---- Inverse DWT: a 32x32 output tile plus a 4-sample apron. ----

static const int TILE = 40;
groupshared float tile[TILE][TILE + 1];

void unliftCols(uint li, uint parity, float c)
{
	for (uint i = li; i < TILE * 20; i += 256)
	{
		int col = i % TILE;
		int y = 2 * (i / TILE) + parity;
		if (y >= 1 && y <= TILE - 2)
			tile[y][col] -= c * (tile[y - 1][col] + tile[y + 1][col]);
	}
	GroupMemoryBarrierWithGroupSync();
}

void unliftRows(uint li, uint parity, float c)
{
	for (uint i = li; i < TILE * 20; i += 256)
	{
		int row = i % TILE;
		int x = 2 * (i / TILE) + parity;
		if (x >= 1 && x <= TILE - 2)
			tile[row][x] -= c * (tile[row][x - 1] + tile[row][x + 1]);
	}
	GroupMemoryBarrierWithGroupSync();
}

// planeSize is the output plane; outOff the four input bands; inOff the
// output plane offset.
[numthreads(16, 16, 1)]
void idwt(uint3 gid : SV_GroupID, uint li : SV_GroupIndex)
{
	int2 base = int2(gid.xy) * 32 - 4;
	int W = planeSize.x, H = planeSize.y, hw = W / 2;
	for (uint i = li; i < TILE * TILE; i += 256)
	{
		int ty = i / TILE, tx = i % TILE;
		int x = mirror(base.x + tx, W), y = mirror(base.y + ty, H);
		uint kind = (x & 1) | ((y & 1) << 1);
		float s = ((x & 1) != 0 ? INV_K : K) * ((y & 1) != 0 ? INV_K : K);
		tile[ty][tx] = loadCoef(outOff[kind] + (y / 2) * hw + x / 2) * s;
	}
	GroupMemoryBarrierWithGroupSync();
	unliftCols(li, 0, DELTA);
	unliftCols(li, 1, GAMMA);
	unliftCols(li, 0, BETA);
	unliftCols(li, 1, ALPHA);
	unliftRows(li, 0, DELTA);
	unliftRows(li, 1, GAMMA);
	unliftRows(li, 0, BETA);
	unliftRows(li, 1, ALPHA);
	for (uint j = li; j < 32 * 32; j += 256)
	{
		int ty = 4 + j / 32, tx = 4 + j % 32;
		int2 p = base + int2(tx, ty);
		if (p.x < W && p.y < H)
			storeCoef(inOff + p.y * W + p.x, tile[ty][tx]);
	}
}

// ---- Colour: DC shift back, upsample chroma, YCbCr to RGB. ----

float chromaAt(uint off, float2 f)
{
	int2 p0 = int2(floor(f));
	float2 t = f - float2(p0);
	int2 last = imageSize / 2 - 1;
	int2 p1 = clamp(p0 + 1, 0, last);
	p0 = clamp(p0, 0, last);
	float a = saturate(loadCoef(off + p0.y * chromaSize.x + p0.x) + 0.5);
	float b = saturate(loadCoef(off + p0.y * chromaSize.x + p1.x) + 0.5);
	float c = saturate(loadCoef(off + p1.y * chromaSize.x + p0.x) + 0.5);
	float d = saturate(loadCoef(off + p1.y * chromaSize.x + p1.x) + 0.5);
	return lerp(lerp(a, b, t.x), lerp(c, d, t.x), t.y);
}

[numthreads(16, 16, 1)]
void yuv2rgb(uint3 id : SV_DispatchThreadID)
{
	int2 p = int2(id.xy);
	if (any(p >= imageSize))
		return;
	float y = saturate(loadCoef(outOff.x + p.y * planeSize.x + p.x) + 0.5);
	float cb, cr;
	if (chroma420 != 0)
	{
		float2 f = float2(p) * 0.5 - 0.25;
		cb = chromaAt(outOff.y, f);
		cr = chromaAt(outOff.z, f);
	}
	else
	{
		cb = saturate(loadCoef(outOff.y + p.y * planeSize.x + p.x) + 0.5);
		cr = saturate(loadCoef(outOff.z + p.y * planeSize.x + p.x) + 0.5);
	}
	cb -= CHROMA_ZERO;
	cr -= CHROMA_ZERO;
	float kg = 1.0 - kr - kb;
	float3 rgb = float3(y + (2.0 - 2.0 * kr) * cr,
		y - (kb * (2.0 - 2.0 * kb) / kg) * cb - (kr * (2.0 - 2.0 * kr) / kg) * cr,
		y + (2.0 - 2.0 * kb) * cb);
	outTex[p] = float4(saturate(rgb), 1.0);
}
