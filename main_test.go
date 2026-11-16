package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommand(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "vgofmt")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	dir := t.TempDir()
	for name, content := range map[string]string{
		"go.mod":    "module example.com/command\ngo 1.26.0\n",
		"sample.go": "package sample\nvar Value=1\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run := func(wantCode int, args ...string) string {
		t.Helper()
		cmd := exec.Command(bin, args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		code := 0
		if err != nil {
			if exit, ok := err.(*exec.ExitError); ok {
				code = exit.ExitCode()
			} else {
				t.Fatal(err)
			}
		}
		if code != wantCode {
			t.Fatalf("%v: exit %d, want %d:\n%s", args, code, wantCode, out)
		}
		return string(out)
	}
	if out := run(0, "-version"); !strings.HasPrefix(out, "vgofmt ") {
		t.Fatalf("version: %q", out)
	}
	run(0, "-h")
	run(2, "-unknown")
	run(2, "-w", "-check")
	before, err := os.ReadFile(filepath.Join(dir, "sample.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{nil, {"-l"}, {"-check"}} {
		code := 0
		if len(args) > 0 && args[0] == "-check" {
			code = 1
		}
		if out := run(code, args...); !strings.Contains(out, "sample.go") {
			t.Fatalf("missing changed path: %q", out)
		}
		after, err := os.ReadFile(filepath.Join(dir, "sample.go"))
		if err != nil || !bytes.Equal(before, after) {
			t.Fatalf("read-only command modified source: %v", err)
		}
	}
	run(0, "-w", "-l")
	if out := run(0, "-check"); out != "" {
		t.Fatalf("second run changed files: %q", out)
	}
	if err := os.WriteFile(filepath.Join(dir, "broken.go"), []byte("package sample\nvar _ = missing\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(2, "-w")
}
