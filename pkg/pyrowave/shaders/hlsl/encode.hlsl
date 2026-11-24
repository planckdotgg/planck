// Copyright (c) 2025 Hans-Kristian Arntzen
// Copyright (c) 2026 The Planck Authors
// SPDX-License-Identifier: MIT
//
// PyroWave encoder for Direct3D 11 (cs_5_0), ported from PyroWave's MIT
// shaders (dwt.comp, wavelet_quant.comp, analyze_rate_control.comp,
// resolve_rate_control.comp, block_packing.comp). Subgroup operations are
// replaced by groupshared reductions and atomics. See LICENSE.pyrowave.
// Concatenated after common.hlsl.

Texture2D<float4> src : register(t0);
RWByteAddressBuffer coef : register(u0);
RWByteAddressBuffer bands : register(u1);
RWByteAddressBuffer stats : register(u2);
RWByteAddressBuffer rc : register(u3);
RWByteAddressBuffer outBuf : register(u4);

// rc layout, in words.
static const uint RC_HIST = 0;
static const uint RC_CONSUMED = NUM_BUCKETS;
static const uint RC_THRESHOLD = NUM_BUCKETS + 1;
static const uint RC_REMAINING = NUM_BUCKETS + 2;
static const uint RC_COUNTER = NUM_BUCKETS + 3;
// outBuf: word 0 is the payload word counter; records start at word 4.
static const uint OUT_PAYLOAD = 16;

void storeCoef(uint i, float v) { coef.Store(i * 4, asuint(v)); }
float loadCoef(uint i) { return asfloat(coef.Load(i * 4)); }

float3 toYCbCr(float3 rgb)
{
	float y = kr * rgb.r + (1.0 - kr - kb) * rgb.g + kb * rgb.b;
	return float3(y, (rgb.b - y) / (2.0 - 2.0 * kb) + CHROMA_ZERO, (rgb.r - y) / (2.0 - 2.0 * kr) + CHROMA_ZERO);
}

float3 fetchRGB(int2 p)
{
	return src.Load(int3(min(p, imageSize - 1), 0)).rgb;
}

// One thread per sample of the aligned chroma plane: 4:2:0 also writes the
// 2x2 luma samples it covers. Past the picture, samples repeat its edge.
[numthreads(16, 16, 1)]
void convert(uint3 id : SV_DispatchThreadID)
{
	int2 c = int2(id.xy);
	if (any(c >= chromaSize))
		return;
	if (chroma420 != 0)
	{
		int2 cc = min(c, imageSize / 2 - 1);
		float3 sum = 0;
		[unroll]
		for (int d = 0; d < 4; d++)
		{
			int2 p = 2 * c + int2(d & 1, d >> 1);
			storeCoef(outOff.x + p.y * planeSize.x + p.x, toYCbCr(fetchRGB(p)).x - 0.5);
			sum += fetchRGB(2 * cc + int2(d & 1, d >> 1));
		}
		float3 ycc = toYCbCr(sum * 0.25);
		uint o = c.y * chromaSize.x + c.x;
		storeCoef(outOff.y + o, ycc.y - 0.5);
		storeCoef(outOff.z + o, ycc.z - 0.5);
	}
	else
	{
		float3 ycc = toYCbCr(fetchRGB(c));
		uint o = c.y * planeSize.x + c.x;
		storeCoef(outOff.x + o, ycc.x - 0.5);
		storeCoef(outOff.y + o, ycc.y - 0.5);
		storeCoef(outOff.z + o, ycc.z - 0.5);
	}
}

// ---- Forward DWT: a 32x32 tile of the input plus a 4-sample apron. ----

static const int TILE = 40;
groupshared float tile[TILE][TILE + 1];

void liftRows(uint li, uint parity, float c)
{
	for (uint i = li; i < TILE * 20; i += 256)
	{
		int row = i % TILE;
		int x = 2 * (i / TILE) + parity;
		if (x >= 1 && x <= TILE - 2)
			tile[row][x] += c * (tile[row][x - 1] + tile[row][x + 1]);
	}
	GroupMemoryBarrierWithGroupSync();
}

void liftCols(uint li, uint parity, float c)
{
	for (uint i = li; i < TILE * 20; i += 256)
	{
		int col = i % TILE;
		int y = 2 * (i / TILE) + parity;
		if (y >= 1 && y <= TILE - 2)
			tile[y][col] += c * (tile[y - 1][col] + tile[y + 1][col]);
	}
	GroupMemoryBarrierWithGroupSync();
}

[numthreads(16, 16, 1)]
void dwt(uint3 gid : SV_GroupID, uint li : SV_GroupIndex)
{
	int2 base = int2(gid.xy) * 32 - 4;
	int W = planeSize.x, H = planeSize.y;
	for (uint i = li; i < TILE * TILE; i += 256)
	{
		int ty = i / TILE, tx = i % TILE;
		int x = mirror(base.x + tx, W), y = mirror(base.y + ty, H);
		tile[ty][tx] = loadCoef(inOff + y * W + x);
	}
	GroupMemoryBarrierWithGroupSync();
	liftRows(li, 1, ALPHA);
	liftRows(li, 0, BETA);
	liftRows(li, 1, GAMMA);
	liftRows(li, 0, DELTA);
	liftCols(li, 1, ALPHA);
	liftCols(li, 0, BETA);
	liftCols(li, 1, GAMMA);
	liftCols(li, 0, DELTA);
	int hw = W / 2, hh = H / 2;
	for (uint j = li; j < 32 * 32; j += 256)
	{
		int ty = 4 + j / 32, tx = 4 + j % 32;
		int2 p = (base + int2(tx, ty)) / 2;
		if (p.x >= hw || p.y >= hh)
			continue;
		float s = ((tx & 1) != 0 ? K : INV_K) * ((ty & 1) != 0 ? K : INV_K);
		uint kind = (tx & 1) | ((ty & 1) << 1);
		storeCoef(outOff[kind] + p.y * hw + p.x, tile[ty][tx] * s);
	}
}

// ---- Quantization shared by analysis and packing. ----
// A group of 128 threads codes one 32x32 block: thread t is subblock t&7 of
// 8x8 block t>>3 (row-major in the 32x32 block).

groupshared uint gsMaxF[16];
groupshared uint gsMax8[16];
groupshared uint gsBlockMax;

struct Quant
{
	float absc[8];
	bool neg[8];
	uint v[8];
	uint subMax;
	uint max8;
	uint code;
	float invScale; // 1 / (qres * 8x8 scale)
};

Quant quantize(BandDesc b, uint block, uint li)
{
	Quant q;
	uint local = block - b.firstBlock;
	uint bx = local % b.blocksX, by = local / b.blocksX;
	uint j = li >> 3, s = li & 7u;
	if (li < 16)
	{
		gsMaxF[li] = 0;
		gsMax8[li] = 0;
	}
	if (li == 0)
		gsBlockMax = 0;
	GroupMemoryBarrierWithGroupSync();
	float m = 0;
	[unroll]
	for (uint p = 0; p < 8; p++)
	{
		int2 xy = unswizzle8x8(s * 8 + p);
		uint X = bx * 32 + (j & 3u) * 8 + xy.x, Y = by * 32 + (j >> 2) * 8 + xy.y;
		float c = 0;
		if (X < b.width && Y < b.height)
			c = loadCoef(b.offset + Y * b.width + X);
		q.absc[p] = abs(c);
		q.neg[p] = c < 0;
		m = max(m, q.absc[p] * b.qres);
	}
	InterlockedMax(gsMaxF[j], asuint(m));
	GroupMemoryBarrierWithGroupSync();
	float m8 = asfloat(gsMaxF[j]);
	float qs = 1.0;
	q.code = 6;
	if (m8 >= 1.0)
	{
		float e;
		frexp(m8 - 0.25, e);
		float target = exp2(e) - 0.25;
		q.code = uint(ceil((m8 / target - 0.25) * 8.0));
		qs = 1.0 / block8x8Scale(q.code);
	}
	q.invScale = 1.0 / (b.qres * qs);
	q.subMax = 0;
	[unroll]
	for (uint p2 = 0; p2 < 8; p2++)
	{
		q.v[p2] = min(uint(q.absc[p2] * b.qres * qs), MAX_MAG);
		q.subMax = max(q.subMax, q.v[p2]);
	}
	InterlockedMax(gsMax8[j], q.subMax);
	InterlockedMax(gsBlockMax, q.subMax);
	GroupMemoryBarrierWithGroupSync();
	q.max8 = gsMax8[j];
	return q;
}

// What this thread's subblock costs after dropping drop planes.
struct Cost
{
	uint planes;
	uint nz;
	bool active; // the 8x8 block is coded
	uint qbits;
	uint code;
	float dist;
};

Cost cost(Quant q, uint drop, uint efield, float rdo)
{
	Cost c;
	uint d = drop > efield ? MAX_DROP : drop;
	uint m = d >= MAX_DROP ? 0 : q.max8 >> d;
	c.active = m != 0;
	c.qbits = c.active ? max(int(bitLen(m)) - 3, 0) : 0;
	c.code = c.active ? bitLen((q.subMax >> d) >> c.qbits) : 0;
	c.planes = c.active ? c.qbits + c.code : 0;
	c.nz = 0;
	c.dist = 0;
	float step = exp2(float(min(d, 31u)));
	[unroll]
	for (uint p = 0; p < 8; p++)
	{
		uint vq = c.active ? q.v[p] >> d : 0;
		float rec = 0;
		if (vq != 0)
		{
			rec = (float(vq) + 0.5) * step * q.invScale;
			c.nz++;
		}
		float e = q.absc[p] - rec;
		c.dist += e * e * rdo;
	}
	return c;
}

uint recordWords(uint n, uint planes, uint signs)
{
	return n == 0 ? 0 : 2 + (3 * n + planes + (signs + 7) / 8 + 3) / 4;
}

// ---- Analysis: words and distortion for every plane drop of a block. ----

groupshared uint gsA[17 * 128];
groupshared float gsD[17 * 128];
groupshared uint gsWords[17];
groupshared float gsDist[17];

[numthreads(128, 1, 1)]
void analyze(uint3 gid : SV_GroupID, uint li : SV_GroupIndex)
{
	uint block = groupBlock(gid);
	if (block >= totalBlocks)
		return;
	BandDesc b = loadBand(bands, findBand(bands, block));
	Quant q = quantize(b, block, li);
	uint efield = b.quantCode >> 3;
	// Past dlim every drop removes the block: same words and distortion.
	uint dlim = min(MAX_DROP, min(bitLen(gsBlockMax), efield + 1));
	for (uint d = 0; d <= dlim; d++)
	{
		Cost c = cost(q, d, efield, b.rdo);
		gsA[d * 128 + li] = c.planes | (c.nz << 12) | (((li & 7u) == 0 && c.active) ? 1u << 23 : 0u);
		gsD[d * 128 + li] = c.dist;
	}
	GroupMemoryBarrierWithGroupSync();
	// Sum 16 threads at a time into the first slot of each run, then the 8 runs.
	for (uint k = li; k < (dlim + 1) * 8; k += 128)
	{
		uint base = (k / 8) * 128 + (k % 8) * 16;
		uint a = 0;
		float dd = 0;
		for (uint i = 0; i < 16; i++)
		{
			a += gsA[base + i];
			dd += gsD[base + i];
		}
		gsA[base] = a;
		gsD[base] = dd;
	}
	GroupMemoryBarrierWithGroupSync();
	if (li <= MAX_DROP)
	{
		uint dl = min(li, dlim);
		uint a = 0;
		float dd = 0;
		for (uint i = 0; i < 8; i++)
		{
			a += gsA[dl * 128 + i * 16];
			dd += gsD[dl * 128 + i * 16];
		}
		gsWords[li] = recordWords(a >> 23, a & 0xfffu, (a >> 12) & 0x7ffu);
		gsDist[li] = dd;
	}
	GroupMemoryBarrierWithGroupSync();
	if (li != 0)
		return;
	uint packedBucket[4] = { 0, 0, 0, 0 };
	uint packedSaving[8] = { 0, 0, 0, 0, 0, 0, 0, 0 };
	uint last = 0, prev = 0;
	for (uint i = 1; i <= MAX_DROP; i++)
	{
		uint bucket = 0;
		uint saving = 0;
		uint s = gsWords[last] - gsWords[i];
		if (s > 0)
		{
			saving = gsWords[i - 1] - gsWords[i];
			float v = 60.0 + 2.0 * log2(max(gsDist[i] - gsDist[last], 0.0) / float(s)) + 0.5;
			bucket = v > 0.0 ? uint(min(v, 1e6)) : 0;
		}
		bucket = min(bucket, NUM_BUCKETS - 1 - MAX_DROP + i);
		bucket = max(bucket, prev + 1);
		prev = bucket;
		if (saving > 0)
		{
			last = i;
			uint dummy;
			rc.InterlockedAdd((RC_HIST + bucket) * 4, saving, dummy);
		}
		packedBucket[(i - 1) / 4] |= bucket << (8 * ((i - 1) % 4));
		packedSaving[(i - 1) / 2] |= saving << (16 * ((i - 1) % 2));
	}
	uint o;
	rc.InterlockedAdd(RC_CONSUMED * 4, gsWords[0], o);
	uint sb = block * STAT_WORDS * 4;
	stats.Store4(sb, uint4(packedBucket[0], packedBucket[1], packedBucket[2], packedBucket[3]));
	stats.Store4(sb + 16, uint4(packedSaving[0], packedSaving[1], packedSaving[2], packedSaving[3]));
	stats.Store4(sb + 32, uint4(packedSaving[4], packedSaving[5], packedSaving[6], packedSaving[7]));
	stats.Store(sb + 48, gsWords[0]);
}

// ---- Rate control: the bucket where savings first cover the excess. ----

groupshared uint gsScan[NUM_BUCKETS];

[numthreads(NUM_BUCKETS, 1, 1)]
void finalize(uint li : SV_GroupIndex)
{
	gsScan[li] = rc.Load((RC_HIST + li) * 4);
	GroupMemoryBarrierWithGroupSync();
	for (uint step = 1; step < NUM_BUCKETS; step *= 2)
	{
		uint up = li >= step ? gsScan[li - step] : 0;
		GroupMemoryBarrierWithGroupSync();
		gsScan[li] += up;
		GroupMemoryBarrierWithGroupSync();
	}
	int required = int(rc.Load(RC_CONSUMED * 4)) - int(budgetWords);
	uint before = li == 0 ? 0 : gsScan[li - 1];
	if (li == 0)
	{
		outBuf.Store(0, 0);
		rc.Store(RC_COUNTER * 4, 0);
		if (required <= 0)
			rc.Store(RC_THRESHOLD * 4, 0xffffffffu);
		else if (gsScan[NUM_BUCKETS - 1] < uint(required))
			rc.Store(RC_THRESHOLD * 4, NUM_BUCKETS);
	}
	if (required > 0 && gsScan[li] >= uint(required) && before < uint(required))
	{
		rc.Store(RC_THRESHOLD * 4, li);
		rc.Store(RC_REMAINING * 4, uint(required) - before);
	}
}

// ---- Packing: quantize at the chosen drop and write the record. ----

groupshared uint gsDrop;
groupshared uint gsBallot;
groupshared uint gsScanP[128];
groupshared uint gsBlk[640];
groupshared uint gsOffset;

void orByte(uint addr, uint v)
{
	InterlockedOr(gsBlk[addr >> 2], (v & 0xffu) << ((addr & 3u) * 8));
}

[numthreads(128, 1, 1)]
void pack(uint3 gid : SV_GroupID, uint li : SV_GroupIndex)
{
	uint block = groupBlock(gid);
	if (block >= totalBlocks)
		return;
	BandDesc b = loadBand(bands, findBand(bands, block));
	if (li == 0)
	{
		uint threshold = rc.Load(RC_THRESHOLD * 4);
		uint remaining = rc.Load(RC_REMAINING * 4);
		uint sb = block * STAT_WORDS * 4;
		uint4 bk = stats.Load4(sb);
		uint4 s0 = stats.Load4(sb + 16);
		uint4 s1 = stats.Load4(sb + 32);
		uint bucketW[4] = { bk.x, bk.y, bk.z, bk.w };
		uint savingW[8] = { s0.x, s0.y, s0.z, s0.w, s1.x, s1.y, s1.z, s1.w };
		uint drop = 0;
		for (uint i = 1; i <= MAX_DROP; i++)
		{
			uint bucket = (bucketW[(i - 1) / 4] >> (8 * ((i - 1) % 4))) & 0xffu;
			uint saving = (savingW[(i - 1) / 2] >> (16 * ((i - 1) % 2))) & 0xffffu;
			if (saving == 0)
				continue;
			if (threshold != 0xffffffffu && bucket < threshold)
				drop = i;
			else if (bucket == threshold)
			{
				uint old;
				rc.InterlockedAdd(RC_COUNTER * 4, saving, old);
				if (old < remaining)
					drop = i;
			}
		}
		gsDrop = drop;
		gsBallot = 0;
	}
	Quant q = quantize(b, block, li);
	uint drop = gsDrop;
	bool coded = drop < MAX_DROP && drop <= (b.quantCode >> 3);
	Cost c = cost(q, coded ? drop : MAX_DROP, b.quantCode >> 3, 0.0);
	uint j = li >> 3, s = li & 7u;
	if (s == 0 && c.active)
		InterlockedOr(gsBallot, 1u << j);
	gsScanP[li] = c.planes | (c.nz << 16);
	GroupMemoryBarrierWithGroupSync();
	for (uint step = 1; step < 128; step *= 2)
	{
		uint up = li >= step ? gsScanP[li - step] : 0;
		GroupMemoryBarrierWithGroupSync();
		gsScanP[li] += up;
		GroupMemoryBarrierWithGroupSync();
	}
	uint ballot = gsBallot;
	uint n = countbits(ballot);
	uint total = gsScanP[127];
	uint P = total & 0xffffu, S = total >> 16;
	uint words = recordWords(n, P, S);
	uint excl = gsScanP[li] - (c.planes | (c.nz << 16));
	for (uint z = li; z < words; z += 128)
		gsBlk[z] = 0;
	GroupMemoryBarrierWithGroupSync();
	if (li == 0 && words != 0)
	{
		uint off;
		outBuf.InterlockedAdd(0, words, off);
		gsOffset = off;
		gsBlk[0] = ballot | (words << 16) | ((seq & 7u) << 28);
		gsBlk[1] = (b.quantCode - (drop << 3)) | (block << 8);
	}
	if (c.active)
	{
		uint k = countbits(ballot & ((1u << j) - 1u));
		if (s == 0)
		{
			orByte(8 + 2 * n + k, c.qbits | (q.code << 4));
		}
		InterlockedOr(gsBlk[(8 + 2 * k) >> 2], (c.code << (2 * s)) << (((8 + 2 * k) & 3u) * 8));
		uint addr = 8 + 3 * n + (excl & 0xffffu);
		for (int pl = int(c.planes) - 1; pl >= 0; pl--)
		{
			uint by = 0;
			[unroll]
			for (uint p = 0; p < 8; p++)
				by |= (((q.v[p] >> drop) >> pl) & 1u) << p;
			orByte(addr++, by);
		}
		uint bit = (8 + 3 * n + P) * 8 + (excl >> 16);
		[unroll]
		for (uint p2 = 0; p2 < 8; p2++)
		{
			if ((q.v[p2] >> drop) != 0)
			{
				if (q.neg[p2])
					InterlockedOr(gsBlk[bit >> 5], 1u << (bit & 31u));
				bit++;
			}
		}
	}
	GroupMemoryBarrierWithGroupSync();
	uint dst = OUT_PAYLOAD + gsOffset * 4;
	for (uint w = li; w < words; w += 128)
		outBuf.Store(dst + w * 4, gsBlk[w]);
}
