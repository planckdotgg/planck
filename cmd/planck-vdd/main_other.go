//go:build !windows

// Command planck-vdd checks the SudoVDA virtual display driver, which only
// exists on Windows.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "planck-vdd: SudoVDA is Windows only")
	os.Exit(1)
}
