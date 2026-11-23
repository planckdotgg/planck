module github.com/planckdotgg/planck

go 1.27.4

require (
	github.com/ebitengine/oto/v3 v3.5.1
	github.com/ebitengine/purego v0.11.3
	github.com/egoist/mygo v0.10.0
	github.com/klauspost/reedsolomon v1.14.2
	github.com/quic-go/quic-go v0.65.0
	github.com/thesyncim/gopus v0.2.4
	golang.org/x/crypto v0.59.0
	golang.org/x/sys v0.50.0
)

require (
	github.com/jfreymuth/pulse v0.1.3 // indirect
	github.com/klauspost/cpuid/v2 v2.3.0 // indirect
	golang.org/x/net v0.62.0 // indirect
)

replace github.com/egoist/mygo => ./third_party/mygo
