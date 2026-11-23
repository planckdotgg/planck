//go:build windows

package main

func init() {
	commands["enum"] = runEnum
	commands["nvenc"] = runNVENC
	commands["dda"] = runDDA
	commands["xfer"] = runXfer
	commands["vendors"] = runVendors
	commands["amf"] = runAMF
	commands["pipeline"] = runPipeline
}
