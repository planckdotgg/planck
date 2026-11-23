// Command planck-spike2 runs the Phase 0 capture and encode measurements.
package main

import (
	"fmt"
	"os"
	"sort"
)

var commands = map[string]func([]string) error{}

func main() {
	if len(os.Args) < 2 || commands[os.Args[1]] == nil {
		names := make([]string, 0, len(commands))
		for n := range commands {
			names = append(names, n)
		}
		sort.Strings(names)
		fmt.Fprintf(os.Stderr, "usage: planck-spike2 <%v> [flags]\n", names)
		os.Exit(2)
	}
	if err := commands[os.Args[1]](os.Args[2:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
