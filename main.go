// Command vgofmt formats Go packages with typed layout rules and gofumpt.
package main

import (
	"context"
	"os"

	"github.com/tiltwind/vgofmt/internal/cli"
)

func main() {
	os.Exit(cli.Run(context.Background(), os.Args[1:], os.Stdout, os.Stderr))
}
