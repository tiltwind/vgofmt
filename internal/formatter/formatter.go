package formatter

import (
	"bytes"
	"context"
	"fmt"
	"go/ast"
	"go/format"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/tools/go/packages"
	gofumpt "mvdan.cc/gofumpt/format"
)

// Options selects packages in the active GOOS/GOARCH and build-tag environment.
type Options struct {
	Dir      string
	Patterns []string
	Tags     string
	Write    bool
}

type change struct {
	path   string
	source []byte
	output []byte
	mode   os.FileMode
}

// Run computes every output before writing any file. It returns sorted changed paths.
func Run(ctx context.Context, opts Options) ([]string, error) {
	patterns := opts.Patterns
	if len(patterns) == 0 {
		patterns = []string{"./..."}
	}
	config := &packages.Config{
		Context: ctx,
		Dir:     opts.Dir,
		Mode:    packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles | packages.NeedSyntax | packages.NeedTypes | packages.NeedTypesInfo | packages.NeedImports | packages.NeedDeps | packages.NeedModule,
		Tests:   true,
	}
	if opts.Tags != "" {
		config.BuildFlags = []string{"-tags=" + opts.Tags}
	}
	pkgs, err := packages.Load(config, patterns...)
	if err != nil {
		return nil, err
	}
	var loadErrors []string
	packages.Visit(pkgs, nil, func(pkg *packages.Package) {
		for _, err := range pkg.Errors {
			loadErrors = append(loadErrors, err.Error())
		}
	})
	if len(loadErrors) > 0 {
		sort.Strings(loadErrors)
		return nil, fmt.Errorf("cannot format packages with load or type errors:\n%s", strings.Join(loadErrors, "\n"))
	}
	if len(pkgs) == 0 {
		return nil, fmt.Errorf("no packages matched %s", strings.Join(patterns, " "))
	}
	seen := make(map[string]bool)
	var changes []change
	for _, pkg := range pkgs {
		originals := make(map[string]bool, len(pkg.GoFiles))
		for _, path := range pkg.GoFiles {
			originals[path] = true
		}
		for _, file := range pkg.Syntax {
			path := pkg.Fset.
				Position(file.Pos()).
				Filename
			if seen[path] ||
				!originals[path] ||
				ast.IsGenerated(file) ||
				inVendor(path) {
				continue
			}
			seen[path] = true
			src, err := os.ReadFile(path)
			if err != nil {
				return nil, err
			}
			out, err := expand(src, file, pkg.Fset, pkg.TypesInfo)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", path, err)
			}
			// Canonicalize even when expand made no edits. The embedded formatter
			// must also handle files containing only whitespace or gofumpt changes.
			out, err = format.Source(out)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", path, err)
			}
			options := gofumpt.Options{}
			if pkg.Module != nil {
				options.ModulePath = pkg.Module.Path
				if pkg.Module.GoVersion != "" {
					options.LangVersion = "go" + pkg.Module.GoVersion
				}
			}
			out, err = gofumpt.Source(out, options)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", path, err)
			}
			if bytes.Equal(src, out) {
				continue
			}
			stat, err := os.Lstat(path)
			if err != nil {
				return nil, err
			}
			if opts.Write && !stat.Mode().IsRegular() {
				return nil, fmt.Errorf("refusing to replace non-regular file %s", path)
			}
			changes = append(changes, change{
				path:   path,
				source: src,
				output: out,
				mode:   stat.Mode().Perm(),
			})
		}
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].path < changes[j].path })
	paths := make([]string, 0, len(changes))
	for _, change := range changes {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if opts.Write {
			if err := writeFile(change); err != nil {
				return nil, fmt.Errorf("%s: %w", change.path, err)
			}
		}
		paths = append(paths, change.path)
	}
	return paths, nil
}

func inVendor(path string) bool {
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		if part == "vendor" {
			return true
		}
	}
	return false
}

func writeFile(change change) error {
	current, err := os.ReadFile(change.path)
	if err != nil {
		return err
	}
	if !bytes.Equal(current, change.source) {
		return fmt.Errorf("file changed while formatting; run vgofmt again")
	}
	file, err := os.CreateTemp(filepath.Dir(change.path), ".vgofmt-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if err := file.Chmod(change.mode); err != nil {
		file.Close()
		return err
	}
	if _, err := file.Write(change.output); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), change.path)
}
