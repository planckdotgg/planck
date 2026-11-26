// Command winres makes the Windows resources (icon and version) of
// planck-client.exe and planck-host.exe, and checks a built exe for them
// (internal/winres).
//
//	go run ./scripts/winres generate [-ico assets/planck-windows.ico]
//	go run ./scripts/winres check [-version X.Y.Z] <exe>
//
// generate writes rsrc_windows_amd64.syso into each command's folder from
// the icon and internal/version. Run it after a change to the icon or the
// version; a test fails while the committed files differ from its output.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/planckdotgg/planck/internal/version"
	"github.com/planckdotgg/planck/internal/winres"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: winres generate [-ico file] | winres check [-version X.Y.Z] <exe>")
	}
	switch args[0] {
	case "generate":
		fs := flag.NewFlagSet("generate", flag.ContinueOnError)
		ico := fs.String("ico", filepath.Join("assets", "planck-windows.ico"), "the app icon")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		for _, t := range winres.Targets {
			b, err := winres.Generate(*ico, t.Exe, version.Version)
			if err != nil {
				return err
			}
			out := filepath.Join(filepath.FromSlash(t.Dir), winres.SysoName)
			if err := os.WriteFile(out, b, 0o644); err != nil {
				return err
			}
			fmt.Println("wrote", out)
		}
		return nil
	case "check":
		fs := flag.NewFlagSet("check", flag.ContinueOnError)
		want := fs.String("version", "", "fail unless the version resource has this version")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if fs.NArg() != 1 {
			return errors.New("usage: winres check [-version X.Y.Z] <exe>")
		}
		exe := fs.Arg(0)
		sizes, info, err := winres.Check(exe)
		if err != nil {
			return fmt.Errorf("%s: %w", exe, err)
		}
		if info.Strings["ProductName"] != "Planck" {
			return fmt.Errorf("%s: ProductName is %q, want Planck", exe, info.Strings["ProductName"])
		}
		if *want != "" && info.Strings["ProductVersion"] != *want {
			return fmt.Errorf("%s: ProductVersion is %q, want %s (run go run ./scripts/winres generate)", exe, info.Strings["ProductVersion"], *want)
		}
		keys := make([]string, 0, len(info.Strings))
		for k := range info.Strings {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		fmt.Printf("%s: icon group 1 with %v px images, version %s\n", exe, sizes, info.Version)
		for _, k := range keys {
			fmt.Printf("  %s: %s\n", k, info.Strings[k])
		}
		return nil
	}
	return fmt.Errorf("unknown command %q", args[0])
}
