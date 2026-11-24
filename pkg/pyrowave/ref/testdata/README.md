# Upstream PyroWave goldens

Each `.pyro` file is one frame as written by upstream PyroWave's Metal
encoder (`metal/`, https://github.com/Themaister/pyrowave at
`a32b05e03d70826b19d4d11ff74fe166ed74371b`): a sequence header followed by
block records, from `pyrowave_encoder_encode_cpu` and
`pyrowave_encoder_packetize` with one packet per frame.

The source picture is `internal/synth` frame N converted by `FromBGRA8`
(BT.709, full range) to 8-bit planes (`planes8` in `golden_test.go`). The
budget and upstream's own decode PSNR against those planes are in
`goldens` in `golden_test.go`. Only these output bitstreams come from
upstream; no upstream code is in this repository.

Upstream notice (MIT):

Copyright (c) 2025 Hans-Kristian Arntzen

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.
