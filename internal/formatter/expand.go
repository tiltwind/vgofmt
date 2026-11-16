// Package formatter applies typed layout rules and embedded gofumpt formatting.
package formatter

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/scanner"
	"go/token"
	"go/types"
	"sort"
	"strings"
)

func expand(src []byte, file *ast.File, fset *token.FileSet, info *types.Info) ([]byte, error) {
	insertions := make(map[int]string)
	deletions := make(map[int]int) // blank break inside a struct field cascade, keyed by start offset
	offset := func(pos token.Pos) int { return fset.Position(pos).Offset }
	newline := func(pos token.Pos) {
		off := offset(pos)
		start := bytes.LastIndexByte(src[:off], '\n') + 1
		if len(bytes.TrimSpace(src[start:off])) != 0 {
			if !strings.Contains(insertions[off], "\n") {
				insertions[off] += "\n"
			}
		}
	}
	conditions := make(map[ast.Expr]bool)
	chains := make(map[ast.Expr]bool)
	ast.Inspect(file, func(node ast.Node) bool {
		if expr, ok := node.(ast.Expr); ok {
			if !conditions[expr] {
				var terms []ast.Expr
				var collect func(ast.Expr)
				collect = func(expr ast.Expr) {
					conditions[expr] = true
					if binary, ok := expr.(*ast.BinaryExpr); ok && (binary.Op == token.LAND || binary.Op == token.LOR) {
						collect(binary.X)
						collect(binary.Y)
						return
					}
					terms = append(terms, expr)
				}
				collect(expr)
				if len(terms) > 2 {
					for _, term := range terms[1:] {
						newline(term.Pos())
					}
				}
			}
			if !chains[expr] {
				var selectors []*ast.SelectorExpr
				called := make(map[*ast.SelectorExpr]bool)
				var collect func(ast.Expr)
				collect = func(expr ast.Expr) {
					chains[expr] = true
					switch expr := expr.(type) {
					case *ast.CallExpr:
						if selector, ok := expr.Fun.(*ast.SelectorExpr); ok {
							called[selector] = true
						}
						collect(expr.Fun)
					case *ast.SelectorExpr:
						collect(expr.X)
						selectors = append(selectors, expr)
					case *ast.IndexExpr:
						collect(expr.X)
					case *ast.IndexListExpr:
						collect(expr.X)
					}
				}
				collect(expr)
				// Break a long selector chain, but keep consecutive struct fields together.
				// A field after a call, such as gorm's Error, still starts its own line.
				for i := 1; i < len(selectors); i++ {
					selector := selectors[i]
					if fieldCascade(info, selectors[i-1], selector, called) {
						if start, end, ok := fieldBreak(src, selector, offset); ok {
							deletions[start] = end
						}
						continue
					}
					if len(selectors) > 2 {
						newline(selector.Sel.Pos())
					}
				}
			}
		}
		lit, ok := node.(*ast.CompositeLit)
		if !ok || len(lit.Elts) <= 2 {
			return true
		}
		typ := info.TypeOf(lit)
		if typ == nil {
			return true
		}
		switch typ.Underlying().(type) {
		case *types.Struct, *types.Map:
		default:
			return true
		}
		for _, field := range lit.Elts {
			newline(field.Pos())
		}
		last := lit.Elts[len(lit.Elts)-1]
		end := offset(last.End())
		// A trailing comma, when present, immediately follows the final expression
		// (possibly with whitespace or comments between them).
		tail := src[end:offset(lit.Rbrace)]
		if !hasComma(tail) {
			insertions[end] = "," + insertions[end]
		}
		newline(lit.Rbrace)
		return true
	})
	if len(insertions) == 0 && len(deletions) == 0 {
		return src, nil
	}
	return rewrite(src, insertions, deletions)
}

func structField(info *types.Info, selector *ast.SelectorExpr) bool {
	selection := selectionOf(info, selector)
	return selection != nil && selection.Kind() == types.FieldVal
}

func fieldCascade(info *types.Info, prev, selector *ast.SelectorExpr, called map[*ast.SelectorExpr]bool) bool {
	return uncalledField(info, prev, called) && uncalledField(info, selector, called)
}

func uncalledField(info *types.Info, selector *ast.SelectorExpr, called map[*ast.SelectorExpr]bool) bool {
	return !called[selector] && structField(info, selector)
}

func selectionOf(info *types.Info, selector *ast.SelectorExpr) *types.Selection {
	if info == nil {
		return nil
	}
	return info.Selections[selector]
}

// fieldBreak returns the blank line break between "." and a field name.
func fieldBreak(src []byte, selector *ast.SelectorExpr, offset func(token.Pos) int) (start, end int, ok bool) {
	dot := offset(selector.X.End())
	name := offset(selector.Sel.Pos())
	if dot < 0 ||
		name > len(src) ||
		dot >= name ||
		src[dot] != '.' {
		return 0, 0, false
	}
	gap := src[dot+1 : name]
	if !bytes.Contains(gap, []byte("\n")) || len(bytes.TrimSpace(gap)) != 0 {
		return 0, 0, false
	}
	return dot + 1, name, true
}

func rewrite(src []byte, insertions map[int]string, deletions map[int]int) ([]byte, error) {
	type edit struct {
		start int
		end   int
		text  string
	}
	edits := make([]edit, 0, len(insertions)+len(deletions))
	for pos, text := range insertions {
		edits = append(edits, edit{
			start: pos,
			end:   pos,
			text:  text,
		})
	}
	for start, end := range deletions {
		edits = append(edits, edit{start: start, end: end})
	}
	sort.Slice(edits, func(i, j int) bool {
		if edits[i].start != edits[j].start {
			return edits[i].start < edits[j].start
		}
		return edits[i].end < edits[j].end
	})
	var out bytes.Buffer
	prev := 0
	for _, edit := range edits {
		if edit.start < prev ||
			edit.end < edit.start ||
			edit.end > len(src) {
			return nil, fmt.Errorf("overlapping format edit at byte %d", edit.start)
		}
		out.Write(src[prev:edit.start])
		out.WriteString(edit.text)
		prev = edit.end
	}
	out.Write(src[prev:])
	return format.Source(out.Bytes())
}

func hasComma(src []byte) bool {
	fset := token.NewFileSet()
	file := fset.AddFile("", -1, len(src))
	var scan scanner.Scanner
	scan.Init(file, src, nil, 0)
	_, tok, _ := scan.Scan()
	return tok == token.COMMA
}
