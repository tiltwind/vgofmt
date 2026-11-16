package formatter

import (
	"bytes"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gofumpt "mvdan.cc/gofumpt/format"
)

func TestExpand(t *testing.T) {
	cases := []struct {
		name   string
		source string
		want   string
	}{
		{
			"two conditions",
			"func f(a, b bool) bool { return a && b }",
			"return a && b",
		},
		{
			"three conditions",
			"func f(a, b, c bool) { if a && b && c {} }",
			"if a &&\n\t\tb &&\n\t\tc {",
		},
		{
			"mixed logical operators",
			"func f(a, b, c bool) bool { return a || b && c }",
			"return a ||\n\t\tb &&\n\t\t\tc",
		},
		{
			"parentheses preserve grouping",
			"func f(a, b, c, d bool) bool { return a && (b || c || d) }",
			"return a && (b ||\n\t\tc ||\n\t\td)",
		},
		{
			"condition comments",
			"func f(a, b, c bool) bool { return a && /* first */ b && // second\nc }",
			"return a && /* first */\n\t\tb && // second\n\t\tc",
		},
		{
			"two dots",
			"var _ = Q{}.Next().Next()",
			"var _ = Q{}.Next().Next()",
		},
		{
			"three dots",
			"var _ = Q{}.Next().Next().Next()",
			"var _ = Q{}.Next().\n\tNext().\n\tNext()",
		},
		{
			"field selectors",
			"var _ = Q{}.Child.Child.Child",
			"var _ = Q{}.Child.Child.Child",
		},
		{
			"broken field selectors",
			"var _ = Q{}.Child.\n\tChild.\n\tChild",
			"var _ = Q{}.Child.Child.Child",
		},
		{
			"field comment keeps its break",
			"var _ = Q{}.Child. /* next */\n\tChild.Child",
			"var _ = Q{}.Child. /* next */\n\tChild.Child",
		},
		{
			"fields before a call chain",
			"var _ = Q{}.Child.Child.Next().Next().Next()",
			"var _ = Q{}.Child.Child.\n\tNext().\n\tNext().\n\tNext()",
		},
		{
			"broken fields before a call chain",
			"var _ = Q{}.Child.\n\tChild.Next().\n\tNext().\n\tNext()",
			"var _ = Q{}.Child.Child.\n\tNext().\n\tNext().\n\tNext()",
		},
		{
			"field after a call chain",
			"var _ = Q{}.Next().Next().Next().Child",
			"var _ = Q{}.Next().\n\tNext().\n\tNext().\n\tChild",
		},
		{
			"indexed struct fields",
			"type Item struct { Spec int }\ntype R struct { Snapshot struct{ Specs []Item } }\nvar _ = R{}.Snapshot.Specs[0].Spec",
			"var _ = R{}.Snapshot.Specs[0].Spec",
		},
		{
			"broken indexed struct fields",
			"type Item struct { Spec int }\ntype R struct { Snapshot struct{ Specs []Item } }\nvar _ = R{}.Snapshot.\n\tSpecs[0].\n\tSpec",
			"var _ = R{}.Snapshot.Specs[0].Spec",
		},
		{
			"struct fields inside a condition",
			"func f(remote struct{ ID, SourceKey, EnvironmentID string }, pub struct{ ReleaseID string; Snapshot struct{ Request struct{ SourceKey, EnvironmentID string } } }) bool {\nreturn remote.ID != pub.ReleaseID || remote.SourceKey != pub.Snapshot.Request.SourceKey || remote.EnvironmentID != pub.Snapshot.Request.EnvironmentID\n}",
			"return remote.ID != pub.ReleaseID ||\n\t\tremote.SourceKey != pub.Snapshot.Request.SourceKey ||\n\t\tremote.EnvironmentID != pub.Snapshot.Request.EnvironmentID",
		},
		{
			"broken struct fields inside a condition",
			"func f(remote struct{ ID, SourceKey, EnvironmentID string }, pub struct{ ReleaseID string; Snapshot struct{ Request struct{ SourceKey, EnvironmentID string } } }) bool {\nreturn remote.ID != pub.ReleaseID ||\nremote.SourceKey != pub.Snapshot.\nRequest.\nSourceKey ||\nremote.EnvironmentID != pub.Snapshot.\nRequest.\nEnvironmentID\n}",
			"return remote.ID != pub.ReleaseID ||\n\t\tremote.SourceKey != pub.Snapshot.Request.SourceKey ||\n\t\tremote.EnvironmentID != pub.Snapshot.Request.EnvironmentID",
		},
		{
			"chain comments",
			"var _ = Q{}.Next(). /* second */ Next().Next()",
			"var _ = Q{}.Next(). /* second */\n\t\t\tNext().\n\t\t\tNext()",
		},
		{
			"nested argument chain",
			"var _ = Q{}.Next(Q{}.Next().Next().Next()).Next().Next()",
			"var _ = Q{}.Next(Q{}.Next().\n\tNext().\n\tNext()).\n\tNext().\n\tNext()",
		},
		{
			"indexed chain",
			"var _ = Q{}.Next().List()[0].Next()",
			"var _ = Q{}.Next().\n\tList()[0].\n\tNext()",
		},
		{
			"two fields",
			"var _ = S{A: 1, B: 2}",
			"var _ = S{A: 1, B: 2}",
		},
		{
			"three fields",
			"var _ = S{A: 1, B: 2, C: 3}",
			"var _ = S{\n\tA: 1,\n\tB: 2,\n\tC: 3,\n}",
		},
		{
			"unkeyed",
			"var _ = S{1, 2, 3}",
			"var _ = S{\n\t1,\n\t2,\n\t3,\n}",
		},
		{
			"two map entries",
			"var _ = map[string]int{\"a\": 1, \"b\": 2}",
			"var _ = map[string]int{\"a\": 1, \"b\": 2}",
		},
		{
			"three map entries",
			"var _ = map[string]int{\"a\": 1, \"b\": 2, \"c\": 3}",
			"var _ = map[string]int{\n\t\"a\": 1,\n\t\"b\": 2,\n\t\"c\": 3,\n}",
		},
		{
			"grouped map entries on multiple lines",
			"func f() map[string]string { return map[string]string{\n\"id\": \"1\", \"intent_id\": \"2\", \"spec_no\": \"S1\",\n\"title\": \"title\", \"status\": \"draft\", \"row_version\": \"1\",\n} }",
			"return map[string]string{\n\t\t\"id\":          \"1\",\n\t\t\"intent_id\":   \"2\",\n\t\t\"spec_no\":     \"S1\",\n\t\t\"title\":       \"title\",\n\t\t\"status\":      \"draft\",\n\t\t\"row_version\": \"1\",\n\t}",
		},
		{
			"named map with comments",
			"type M map[string]int\nvar _ = M{\"a\": 1, /* first */ \"b\": 2, \"c\": 3 /* last */}",
			"var _ = M{\n\t\"a\": 1, /* first */\n\t\"b\": 2,\n\t\"c\": 3, /* last */\n}",
		},
		{
			"nested map with omitted type",
			"var _ = map[string]map[string]int{\"nested\": {\"a\": 1, \"b\": 2, \"c\": 3}}",
			"\"nested\": {\n\t\"a\": 1,\n\t\"b\": 2,\n\t\"c\": 3,\n}}",
		},
		{
			"slice",
			"var _ = []int{1, 2, 3}",
			"var _ = []int{1, 2, 3}",
		},
		{
			"array",
			"var _ = [3]int{1, 2, 3}",
			"var _ = [3]int{1, 2, 3}",
		},
		{
			"nested omitted type",
			"var _ = []S{{A: 1, B: 2, C: 3}}",
			"var _ = []S{{\n\tA: 1,\n\tB: 2,\n\tC: 3,\n}}",
		},
		{
			"anonymous",
			"var _ = struct{ A, B, C int }{1, 2, 3}",
			"{\n\t1,\n\t2,\n\t3,\n}",
		},
		{
			"comment",
			"var _ = S{A: 1, /* first */ B: 2, C: 3 /* last */}",
			"C: 3, /* last */\n}",
		},
		{
			"trailing comma",
			"var _ = S{A: 1, B: 2, C: 3,}",
			"C: 3,\n}",
		},
		{
			"line comment",
			"var _ = S{A: 1, // first\nB: 2, C: 3}",
			"A: 1, // first\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := []byte("package sample\ntype S struct { A, B, C int }\ntype Q struct { Child *Q }\nfunc (q Q) Next(...Q) Q { return q }\nfunc (q Q) List() []Q { return nil }\n" + tc.source + "\n")
			apply := func(src []byte) []byte {
				t.Helper()
				fset := token.NewFileSet()
				file, err := parser.ParseFile(fset, "sample.go", src, parser.ParseComments)
				if err != nil {
					t.Fatal(err)
				}
				info := &types.Info{
					Types:      make(map[ast.Expr]types.TypeAndValue),
					Selections: make(map[*ast.SelectorExpr]*types.Selection),
				}
				config := types.Config{}
				if _, err := config.Check("sample", fset, []*ast.File{file}, info); err != nil {
					t.Fatal(err)
				}
				out, err := expand(src, file, fset, info)
				if err != nil {
					t.Fatal(err)
				}
				return out
			}
			got := apply(src)
			goldenPath := filepath.Join("testdata", "expand", strings.ReplaceAll(tc.name, " ", "_")+".golden")
			want, err := os.ReadFile(goldenPath)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("output differs from %s:\n%s", goldenPath, got)
			}
			if !strings.Contains(string(got), tc.want) {
				t.Fatalf("want %q in:\n%s", tc.want, got)
			}
			if again := apply(got); !bytes.Equal(got, again) {
				t.Fatalf("not idempotent:\n%s", again)
			}
			finish := func(src []byte) []byte {
				t.Helper()
				out, err := format.Source(src)
				if err != nil {
					t.Fatal(err)
				}
				out, err = gofumpt.Source(out, gofumpt.Options{LangVersion: "go1.26.0", ModulePath: "sample"})
				if err != nil {
					t.Fatal(err)
				}
				return out
			}
			combined := finish(got)
			if again := finish(apply(combined)); !bytes.Equal(combined, again) {
				t.Fatalf("combined pipeline not idempotent:\n%s", again)
			}
		})
	}
}
