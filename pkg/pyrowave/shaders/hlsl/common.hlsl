// Copyright (c) 2025 Hans-Kristian Arntzen
// Copyright (c) 2026 The Planck Authors
// SPDX-License-Identifier: MIT
//
// Ported from PyroWave (https://github.com/Themaister/pyrowave, MIT):
// dwt_common.h, dwt_swizzle.h, dwt_quant_scale.h. See LICENSE.pyrowave.

static const float ALPHA = -1.586134342059924;
static const float BETA = -0.052980118572961;
static const float GAMMA = 0.882911075530934;
static const float DELTA = 0.443506852043971;
static const float K = 1.230174104914001;
static const float INV_K = 1.0 / 1.230174104914001;

static const uint MAX_DROP = 16;
static const uint NUM_BUCKETS = 128;
static const uint MAX_MAG = (1u << 18) - 1u;
static const uint BAND_WORDS = 12;
static const uint STAT_WORDS = 16;
static const float CHROMA_ZERO = 128.0 / 255.0;

cbuffer Params : register(b0)
{
	int2 imageSize;   // visible picture size
	int2 planeSize;   // convert: luma plane; dwt: input plane
	uint inOff;       // dwt: input plane offset, in floats
	uint chroma420;
	uint totalBlocks;
	uint budgetWords;
	uint4 outOff;     // dwt: band offsets; convert, yuv2rgb: plane offsets
	uint seq;
	uint numBands;
	float kr;
	float kb;
	int2 chromaSize;  // convert, yuv2rgb: chroma plane size
	uint groupsX;
	uint tenBit;
};

// Whole-sample symmetric extension, repeated so any index stays in range.
int mirror(int i, int n)
{
	if (uint(i) < uint(n))
		return i;
	int p = 2 * n - 2;
	i = abs(i) % p;
	return i >= n ? p - i : i;
}

int2 unswizzle8x8(uint i)
{
	uint y = (i & 1u) | (((i >> 3) & 3u) << 1);
	uint x = ((i >> 1) & 3u) | (((i >> 5) & 1u) << 2);
	return int2(x, y);
}

float block8x8Scale(uint code)
{
	return float(code) / 8.0 + 0.25;
}

float block32x32Scale(uint code)
{
	int e = 4 - int(code >> 3);
	float m = float(8u + (code & 7u));
	return m * exp2(float(20 + e)) / (8.0 * 1024.0 * 1024.0);
}

uint bitLen(uint v)
{
	return v == 0 ? 0 : firstbithigh(v) + 1;
}

// Band table: 12 words per band in block_index order.
struct BandDesc
{
	uint offset;     // in floats
	uint width;
	uint height;
	uint blocksX;
	uint firstBlock;
	uint blocks;
	uint quantCode;
	float qres;
	float rdo;
};

BandDesc loadBand(RWByteAddressBuffer t, uint i)
{
	uint base = i * BAND_WORDS * 4;
	BandDesc b;
	b.offset = t.Load(base + 0);
	b.width = t.Load(base + 4);
	b.height = t.Load(base + 8);
	b.blocksX = t.Load(base + 12);
	b.firstBlock = t.Load(base + 16);
	b.blocks = t.Load(base + 20);
	b.quantCode = t.Load(base + 24);
	b.qres = asfloat(t.Load(base + 28));
	b.rdo = asfloat(t.Load(base + 32));
	return b;
}

uint findBand(RWByteAddressBuffer t, uint block)
{
	uint lo = 0, hi = numBands;
	while (lo < hi)
	{
		uint m = (lo + hi) / 2;
		uint base = m * BAND_WORDS * 4;
		if (t.Load(base + 16) + t.Load(base + 20) <= block)
			lo = m + 1;
		else
			hi = m;
	}
	return lo;
}

uint groupBlock(uint3 gid)
{
	return gid.y * groupsX + gid.x;
}
