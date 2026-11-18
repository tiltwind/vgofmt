// Package formatter applies typed layout rules and embedded gofumpt formatting.
package formatter

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/scanner"
	"go/token"
	"go/types"
	"sort"
	"strings"
	"unicode/utf8"
)

// maxLineWidth is the maximum line width that keeps a logical expression on one
// line. Wider layouts are split after logical operators.
const maxLineWidth = 80

func expand(src []byte, file *ast.File, fset *token.FileSet, info *types.Info) ([]byte, error) {
	insertions := make(map[int]string)
	deletions := make(map[int]int) // existing line break after a dot or logical operator
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
	hasCondition := false
	hasChain := false
	ast.Inspect(file, func(node ast.Node) bool {
		if expr, ok := node.(ast.Expr); ok {
			if !conditions[expr] {
				terms, _ := collectCondition(expr, conditions)
				hasCondition = hasCondition || len(terms) > 2
			}
			if !chains[expr] {
				selectors, called := collectChain(expr, chains)
				if len(selectors) > 0 {
					// The layout pass never splits chains with at most two
					// calls, so only longer chains need it.
					hasChain = hasChain || len(called) > 2
					// Object references stay together, so any existing break
					// between a dot and its selector is a candidate for joining.
					// The chain pass re-adds the breaks that are still required.
					for _, selector := range selectors {
						if start, end, ok := selectorBreak(src, selector, offset); ok {
							deletions[start] = end
						}
					}
				}
			}
		}
		if binary, ok := node.(*ast.BinaryExpr); ok && isLogical(binary.Op) {
			if start, end, ok := operatorBreak(src, binary, offset); ok {
				deletions[start] = end
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
	changed := len(insertions) > 0 || len(deletions) > 0
	var base []byte
	if changed {
		var err error
		base, err = rewrite(src, insertions, deletions)
		if err != nil {
			return nil, err
		}
	} else {
		base = src
	}
	if !hasCondition && !hasChain {
		if changed {
			return base, nil
		}
		return src, nil
	}
	// Width must be measured on canonical source, after literals and existing
	// breaks have been expanded or joined, but before any new break is inserted.
	measure := base
	if !changed {
		formatted, err := format.Source(src)
		if err != nil {
			return nil, err
		}
		measure = formatted
	}
	stage := measure
	stageChanged := false
	if hasCondition {
		breaks, err := conditionBreaks(measure)
		if err != nil {
			return nil, err
		}
		if len(breaks) > 0 {
			stage, err = rewrite(measure, breaks, nil)
			if err != nil {
				return nil, err
			}
			stageChanged = true
		}
	}
	if !hasChain {
		if stageChanged {
			return stage, nil
		}
		if changed {
			return base, nil
		}
		return src, nil
	}
	breaks, err := chainBreaks(stage)
	if err != nil {
		return nil, err
	}
	if len(breaks) > 0 {
		return rewrite(stage, breaks, nil)
	}
	if stageChanged {
		return stage, nil
	}
	if changed {
		return base, nil
	}
	return src, nil
}

// isLogical reports whether op combines two boolean terms.
func isLogical(op token.Token) bool {
	return op == token.LAND || op == token.LOR
}

// collectCondition flattens a conjunction or disjunction tree into its terms and
// the positions of the operators between them. Nested groups inside parentheses
// are terms, so they are treated as independent conditions later.
func collectCondition(expr ast.Expr, conditions map[ast.Expr]bool) ([]ast.Expr, []token.Pos) {
	var terms []ast.Expr
	var ops []token.Pos
	var collect func(ast.Expr)
	collect = func(expr ast.Expr) {
		conditions[expr] = true
		if binary, ok := expr.(*ast.BinaryExpr); ok && isLogical(binary.Op) {
			ops = append(ops, binary.OpPos)
			collect(binary.X)
			collect(binary.Y)
			return
		}
		terms = append(terms, expr)
	}
	collect(expr)
	return terms, ops
}

// operatorBreak returns the whitespace-only line break between a logical
// operator and its right operand.
func operatorBreak(src []byte, binary *ast.BinaryExpr, offset func(token.Pos) int) (start, end int, ok bool) {
	start = offset(binary.OpPos) + len(binary.Op.String())
	end = offset(binary.Y.Pos())
	if start < 0 || end > len(src) || start >= end {
		return 0, 0, false
	}
	gap := src[start:end]
	if !bytes.Contains(gap, []byte("\n")) || len(bytes.TrimSpace(gap)) != 0 {
		return 0, 0, false
	}
	return start, end, true
}

func collectChain(expr ast.Expr, chains map[ast.Expr]bool) ([]*ast.SelectorExpr, map[*ast.SelectorExpr]bool) {
	var selectors []*ast.SelectorExpr
	called := make(map[*ast.SelectorExpr]bool)
	var collect func(ast.Expr)
	collect = func(expr ast.Expr) {
		chains[expr] = true
		switch expr := expr.(type) {
		case *ast.CallExpr:
			if selector := calleeSelector(expr.Fun); selector != nil {
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
	return selectors, called
}

// calleeSelector returns the selector invoked by fun, including generic method
// calls such as x.M[T]() and (*T).M(x).
func calleeSelector(fun ast.Expr) *ast.SelectorExpr {
	switch fun := fun.(type) {
	case *ast.SelectorExpr:
		return fun
	case *ast.IndexExpr:
		return calleeSelector(fun.X)
	case *ast.IndexListExpr:
		return calleeSelector(fun.X)
	}
	return nil
}

// conditionBreaks parses canonical source and inserts a newline before every
// term of a logical expression with more than two terms when one of its lines is
// wider than maxLineWidth. Shorter expressions stay on a single line.
func conditionBreaks(src []byte) (map[int]string, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "", src, parser.ParseComments)
	if err != nil {
		return nil, err
	}
	widths := lineWidths(src)
	breaks := make(map[int]string)
	conditions := make(map[ast.Expr]bool)
	ast.Inspect(file, func(node ast.Node) bool {
		expr, ok := node.(ast.Expr)
		if !ok || conditions[expr] {
			return true
		}
		terms, ops := collectCondition(expr, conditions)
		if len(terms) <= 2 {
			return true
		}
		wide := false
		for _, op := range ops {
			if lineTooWide(fset, widths, op) {
				wide = true
				break
			}
		}
		if !wide {
			return true
		}
		for _, term := range terms[1:] {
			addBreak(src, fset, breaks, term.Pos())
		}
		return true
	})
	return breaks, nil
}

// chainBreaks parses canonical source and inserts a newline before every call
// that needs its own line. Only chains with more than two calls are split; chain
// width never adds a break. The receiver, its first call, and all struct field
// selections remain together.
func chainBreaks(src []byte) (map[int]string, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "", src, parser.ParseComments)
	if err != nil {
		return nil, err
	}
	breaks := make(map[int]string)
	chains := make(map[ast.Expr]bool)
	ast.Inspect(file, func(node ast.Node) bool {
		expr, ok := node.(ast.Expr)
		if !ok || chains[expr] {
			return true
		}
		selectors, called := collectChain(expr, chains)
		if len(selectors) == 0 {
			return true
		}
		breakBefore := make([]bool, len(selectors))
		calls := 0
		for i, selector := range selectors {
			if !called[selector] {
				continue
			}
			breakBefore[i] = calls > 0
			calls++
		}
		if calls <= 2 {
			return true
		}
		for i, selector := range selectors {
			if breakBefore[i] {
				addBreak(src, fset, breaks, selector.Sel.Pos())
			}
		}
		return true
	})
	return breaks, nil
}

func lineTooWide(fset *token.FileSet, widths []int, pos token.Pos) bool {
	line := fset.Position(pos).Line
	return line > 0 && line <= len(widths) && widths[line-1] > maxLineWidth
}

func lineWidths(src []byte) []int {
	widths := make([]int, 0, bytes.Count(src, []byte("\n"))+1)
	start := 0
	for i, b := range src {
		if b == '\n' {
			widths = append(widths, utf8.RuneCount(src[start:i]))
			start = i + 1
		}
	}
	return append(widths, utf8.RuneCount(src[start:]))
}

func addBreak(src []byte, fset *token.FileSet, breaks map[int]string, pos token.Pos) {
	off := fset.Position(pos).Offset
	start := bytes.LastIndexByte(src[:off], '\n') + 1
	if len(bytes.TrimSpace(src[start:off])) == 0 {
		return
	}
	if !strings.Contains(breaks[off], "\n") {
		breaks[off] += "\n"
	}
}

// selectorBreak returns the whitespace-only line break between "." and a
// selector name.
func selectorBreak(src []byte, selector *ast.SelectorExpr, offset func(token.Pos) int) (start, end int, ok bool) {
	dot := offset(selector.X.End())
	name := offset(selector.Sel.Pos())
	if dot < 0 || name > len(src) || dot >= name || src[dot] != '.' {
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
		if edit.start < prev || edit.end < edit.start || edit.end > len(src) {
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
