# vgofmt

Go package formatter combining type-aware layout rules with embedded
[gofumpt](https://github.com/mvdan/gofumpt). Requires Go 1.26 or later and a
loadable, type-correct Go module. No separate gofumpt installation is needed.

## Install and run

```sh
go install github.com/tiltwind/vgofmt@latest
vgofmt -w ./...
```

For reproducible project tooling, pin a released version:

```sh
go get -tool github.com/tiltwind/vgofmt@v0.1.0
go tool vgofmt -w ./...
go tool vgofmt -check ./...
```

| Option | Behavior |
| --- | --- |
| no options / `-l` | List files that would change, without writing |
| `-w` | Write changes and list changed files |
| `-check` | List differences without writing; exit 1 if differences exist |
| `-tags special,integration` | Load packages with the specified build tags |
| `-version` | Print the installed build version |

Exit codes: 0 for success, 1 for formatting differences in check mode, 2 for
invalid arguments, package/type errors, or formatting/write errors. `-w` and
`-check` are mutually exclusive. Flags precede package patterns. Patterns default
to `./...`; use `./pkg/...` to restrict the scope.

## Formatting rules

- Struct and map literals with more than two elements use one element per line.
  Named types and nested literals with omitted types are recognized using Go
  type information. Slices and arrays are not expanded by this rule.
- Logical expressions with more than two terms are split after operators only
  when a line they occupy exceeds 80 characters; shorter expressions stay on one
  line. Whitespace-only line breaks after logical operators are joined first.
  Parentheses and operator precedence are preserved.
- Selector chains are split only when they contain more than two calls, or when
  a line they occupy exceeds 80 characters. When split, the receiver, its first
  call, and all struct field selections stay on the opening line; each further
  call starts a new line after the dot. Whitespace-only line breaks between a dot
  and its selector are joined, so object references such as `db.DB` and
  `aa.Error` are never broken. Line width is measured after literal expansion,
  with each tab counting as one character. Comments keep their line breaks.
- Every selected source file is normalized with `go/format`, then gofumpt
  **v0.12.0**, with extra rules disabled. The module's language version and path
  are passed to gofumpt.

## Scope and safety

Package loading includes ordinary and external test packages. Generated source
files and vendor directories are skipped. `./...` does not cross nested module
boundaries or select `testdata` directories. Explicit package patterns can select
test fixtures; generated files and vendor remain excluded.

The active `GOOS`, `GOARCH`, `CGO_ENABLED`, `GOFLAGS`, workspace, and build tags
determine which files are loaded. A single run does not cover all platform or tag
variants. Compiler-generated cgo files are excluded; original files containing
`import "C"` are not currently formatted by this tool. Run platform/tag variants
separately when needed. File arguments, stdin and editor integration are outside
the first release's package-based interface.

All selected packages must load and type-check before any source is written.
Dependencies must be available; missing imports or dependencies are reported as
errors. vgofmt does not add imports or run `go fix`.

All outputs are calculated before writing. Each changed file is replaced using
a temporary file in the same directory, preserving permission bits. Symlinks
requiring changes are refused in write mode. Changes made to a file after its
contents were read are checked again before replacement. Writes across multiple
files are not transactional: an I/O failure can leave earlier files formatted.

## Project integration

Keep import repair separate and run it first:

```makefile
.PHONY: format check-format fix

format:
	goimports -w -l .
	go tool vgofmt -w ./...

check-format:
	go tool vgofmt -check ./...

fix:
	go fix ./...
```

`goimports` must be installed separately. Its directory traversal and generated
file handling differ from vgofmt's package-based scope. Check mode verifies
vgofmt formatting, not missing import repair. `go fix` is a separate code
maintenance action; run `make format` after it.

## Development

```sh
make format      # rewrite this repository with vgofmt itself
make check-format
go test ./...
go vet ./...
go build .
```

Tests cover the extracted layout rules with complete expected outputs,
pipeline formatting and idempotence, cross-file types and external tests,
generated and excluded files, build tags, strict load failures, permission
preservation, and real CLI exit codes and read-only behavior.
