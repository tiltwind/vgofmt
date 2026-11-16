// Package cli implements the vgofmt command line.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"runtime/debug"

	"github.com/tiltwind/vgofmt/internal/formatter"
)

// Run returns 0 on success, 1 for check differences, and 2 for errors.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("vgofmt", flag.ContinueOnError)
	flags.SetOutput(stderr)
	write := flags.Bool("w", false, "write formatted files back to disk")
	flags.Bool("l", false, "list changed files (also the default)")
	check := flags.Bool("check", false, "report differences without writing; exit 1 if any")
	tags := flags.String("tags", "", "build tags passed to go list")
	version := flags.Bool("version", false, "print build version")
	flags.Usage = func() {
		fmt.Fprintln(stderr, "Usage: vgofmt [-w | -check] [-l] [-tags tags] [package patterns]")
		fmt.Fprintln(stderr, "Defaults to ./...; includes tests, skips generated code and vendor.")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if *version {
		value := "devel"
		if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" {
			value = info.Main.Version
		}
		fmt.Fprintln(stdout, "vgofmt", value)
		return 0
	}
	if *write && *check {
		fmt.Fprintln(stderr, "vgofmt: -w and -check cannot be combined")
		return 2
	}
	// All modes report changed paths; -l is accepted for gofmt-style invocation.
	changed, err := formatter.Run(ctx, formatter.Options{
		Patterns: flags.Args(),
		Tags:     *tags,
		Write:    *write,
	})
	if err != nil {
		fmt.Fprintln(stderr, "vgofmt:", err)
		return 2
	}
	for _, path := range changed {
		fmt.Fprintln(stdout, path)
	}
	if *check && len(changed) > 0 {
		return 1
	}
	return 0
}
