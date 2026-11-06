package indexer

import (
	"strings"
	"unicode/utf8"

	"github.com/bytebase/omni/oracle/ast"
	"github.com/bytebase/omni/oracle/parser"
	"github.com/sourcegraph/scip/bindings/go/scip"
)

// constantSQL accepts only literal concatenations. Anything involving a
// variable, function call, or non-concatenation operator remains opaque.
type literalOffsets struct{ starts, ends []int }

func constantSQL(src *source, expr ast.Node) (string, literalOffsets, bool) {
	switch n := expr.(type) {
	case *ast.StringLiteral:
		return stringLiteralSQL(src.text, n)
	case *ast.ParenExpr:
		return constantSQL(src, n.Expr)
	case *ast.BinaryExpr:
		if n.Op != "||" {
			return "", literalOffsets{}, false
		}
		left, lm, ok := constantSQL(src, n.Left)
		if !ok {
			return "", literalOffsets{}, false
		}
		right, rm, ok := constantSQL(src, n.Right)
		if !ok {
			return "", literalOffsets{}, false
		}
		if len(left) == 0 {
			return right, rm, true
		}
		if len(right) == 0 {
			return left, lm, true
		}
		lm.starts[len(lm.starts)-1] = rm.starts[0]
		return left + right, literalOffsets{append(lm.starts, rm.starts[1:]...), append(lm.ends, rm.ends[1:]...)}, true
	default:
		return "", literalOffsets{}, false
	}
}

func stringLiteralSQL(text string, lit *ast.StringLiteral) (string, literalOffsets, bool) {
	if lit == nil || lit.Loc.Start < 0 || lit.Loc.End > len(text) || lit.Loc.End <= lit.Loc.Start {
		return "", literalOffsets{}, false
	}
	raw := text[lit.Loc.Start:lit.Loc.End]
	open := 0
	qQuote := false
	var close byte
	if len(raw) >= 3 && (raw[0] == 'q' || raw[0] == 'Q') && raw[1] == '\'' {
		qQuote = true
		open = 3
		close = raw[2]
		switch close {
		case '[':
			close = ']'
		case '{':
			close = '}'
		case '(':
			close = ')'
		case '<':
			close = '>'
		}
	} else {
		quote := strings.IndexByte(raw, '\'')
		if quote < 0 {
			return "", literalOffsets{}, false
		}
		open = quote + 1
	}
	if open > len(raw) {
		return "", literalOffsets{}, false
	}
	contentEnd := len(raw)
	if qQuote {
		if contentEnd < open+2 || raw[contentEnd-2] != close || raw[contentEnd-1] != '\'' {
			return "", literalOffsets{}, false
		}
		contentEnd -= 2
	} else {
		if contentEnd <= open || raw[contentEnd-1] != '\'' {
			return "", literalOffsets{}, false
		}
		contentEnd--
	}

	var value strings.Builder
	value.Grow(len(lit.Val))
	positions := []int{lit.Loc.Start + open}
	for i := open; i < contentEnd; {
		if !qQuote && raw[i] == '\'' && i+1 < contentEnd && raw[i+1] == '\'' {
			value.WriteByte('\'')
			positions = append(positions, lit.Loc.Start+i+2)
			i += 2
			continue
		}
		_, size := utf8DecodeRuneInString(raw[i:contentEnd])
		if size < 1 {
			return "", literalOffsets{}, false
		}
		value.WriteString(raw[i : i+size])
		for j := 1; j <= size; j++ {
			positions = append(positions, lit.Loc.Start+i+j)
		}
		i += size
	}
	if value.String() != lit.Val || len(positions) != len(lit.Val)+1 {
		return "", literalOffsets{}, false
	}
	// SQL*Plus joins continued lines before executing the literal. Retain
	// separate start/end boundaries around removed bytes so SCIP ranges on
	// either side, including identifiers spanning the join, remain accurate.
	decoded := value.String()
	var joined strings.Builder
	offsets := literalOffsets{starts: []int{positions[0]}, ends: []int{positions[0]}}
	for i := 0; i < len(decoded); {
		if decoded[i] == '-' {
			j := i + 1
			for j < len(decoded) && (decoded[j] == ' ' || decoded[j] == '\t' || decoded[j] == '\r') {
				j++
			}
			if j < len(decoded) && decoded[j] == '\n' {
				i = j + 1
				continue
			}
		}
		offsets.starts[len(offsets.starts)-1] = positions[i]
		joined.WriteByte(decoded[i])
		offsets.starts = append(offsets.starts, positions[i+1])
		offsets.ends = append(offsets.ends, positions[i+1])
		i++
	}
	return joined.String(), offsets, true
}

// utf8DecodeRuneInString avoids converting source bytes or changing offsets.
func utf8DecodeRuneInString(s string) (rune, int) {
	return utf8.DecodeRuneInString(s)
}

func (s *state) resolveDynamicSQL(src *source, expr ast.Node, schema, currentPackage string, start, end int, emit addReference, emitAmbiguous func(int, int, []string)) {
	query, positions, ok := constantSQL(src, expr)
	if !ok || strings.TrimSpace(query) == "" || len(positions.starts) != len(query)+1 || len(positions.ends) != len(query)+1 {
		src.addDiagnostic(start, end, &scip.Diagnostic{Severity: scip.Severity_Warning, Code: "dynamic-sql-opaque", Source: "scip-plsql", Message: "No se pudo indexar el SQL dinámico porque no es una concatenación constante de literales"})
		return
	}
	parsed, err := parser.Parse(query)
	if err != nil {
		src.addDiagnostic(start, end, &scip.Diagnostic{Severity: scip.Severity_Warning, Code: "dynamic-sql-parse", Source: "scip-plsql", Message: err.Error()})
		return
	}
	dynamic := &source{path: src.path, text: query, doc: src.doc, lineStarts: src.lineStarts, offsetMap: positions.starts, nextLocal: src.nextLocal, sqlBindings: src.sqlBindings}
	for _, item := range parsed.Items {
		if raw, ok := item.(*ast.RawStmt); ok && raw.Stmt != nil {
			dynamic.units = append(dynamic.units, unit{node: raw.Stmt, schema: schema})
		}
	}
	s.resolveLocals(dynamic)
	mapOffset := func(offset int, offsets []int) int {
		if offset < 0 {
			offset = 0
		}
		if offset >= len(offsets) {
			offset = len(offsets) - 1
		}
		return offsets[offset]
	}
	mappedEmit := func(a, b int, symbol string, role scip.SymbolRole) {
		emit(mapOffset(a, positions.starts), mapOffset(b, positions.ends), symbol, role)
	}
	mappedAmbiguous := func(a, b int, symbols []string) {
		emitAmbiguous(mapOffset(a, positions.starts), mapOffset(b, positions.ends), symbols)
	}
	for _, item := range parsed.Items {
		raw, ok := item.(*ast.RawStmt)
		if !ok || raw.Stmt == nil {
			continue
		}
		dml := dmlResolver{state: s, src: dynamic, schema: schema, emit: mappedEmit, handledSelect: map[*ast.SelectStmt]bool{}, handledObjects: map[*ast.ObjectName]bool{}}
		supported := true
		switch stmt := raw.Stmt.(type) {
		case *ast.SelectStmt:
			s.resolveSelect(dynamic, stmt, schema, nil, mappedEmit)
			dml.handledSelect[stmt] = true
		case *ast.InsertStmt:
			dml.insert(stmt)
		case *ast.UpdateStmt:
			dml.update(stmt)
		case *ast.DeleteStmt:
			dml.delete(stmt)
		case *ast.MergeStmt:
			dml.merge(stmt)
		case *ast.PLSQLBlock:
			// The block itself is not indexed as a new document; its embedded
			// SQL statements and calls are resolved against the outer catalog.
		default:
			supported = false
		}
		if !supported {
			src.addDiagnostic(start, end, &scip.Diagnostic{Severity: scip.Severity_Warning, Code: "dynamic-sql-unsupported", Source: "scip-plsql", Message: "El SQL dinámico literal se parseó, pero su tipo de sentencia no está soportado"})
			continue
		}
		ast.Inspect(raw.Stmt, func(node ast.Node) bool {
			switch n := node.(type) {
			case *ast.SelectStmt:
				if !dml.handledSelect[n] {
					s.resolveSelect(dynamic, n, schema, nil, mappedEmit)
					dml.handledSelect[n] = true
				}
				return false
			case *ast.InsertStmt:
				if n != raw.Stmt {
					dml.insert(n)
				}
				return false
			case *ast.UpdateStmt:
				if n != raw.Stmt {
					dml.update(n)
				}
				return false
			case *ast.DeleteStmt:
				if n != raw.Stmt {
					dml.delete(n)
				}
				return false
			case *ast.MergeStmt:
				if n != raw.Stmt {
					dml.merge(n)
				}
				return false
			}
			return true
		})
		ast.Inspect(raw.Stmt, func(node ast.Node) bool {
			switch n := node.(type) {
			case *ast.FuncCallExpr:
				if n.FuncName != nil {
					s.resolveCall(dynamic, n, schema, currentPackage, mappedEmit, mappedAmbiguous)
				}
			case *ast.PLSQLCall:
				if name, ok := n.Name.(*ast.ColumnRef); ok {
					call := &ast.FuncCallExpr{FuncName: &ast.ObjectName{Loc: name.Loc}}
					s.resolveCall(dynamic, call, schema, currentPackage, mappedEmit, mappedAmbiguous)
				}
			}
			return true
		})
	}
	src.nextLocal = dynamic.nextLocal
}

func (s *state) resolveCallsInAST(src *source, node ast.Node, schema, currentPackage string, emit addReference, emitAmbiguous func(int, int, []string)) {
	if node == nil {
		return
	}
	ast.Inspect(node, func(node ast.Node) bool {
		switch n := node.(type) {
		case *ast.FuncCallExpr:
			if receiver, ok := src.typedCalls[n]; ok {
				s.resolveInstanceCall(src, n, receiver, emit, emitAmbiguous)
			} else if n.FuncName != nil && !src.shadowedCalls[n] {
				s.resolveCall(src, n, schema, currentPackage, emit, emitAmbiguous)
			}
		case *ast.PLSQLCall:
			if receiver, ok := src.typedProcedureCalls[n]; ok {
				if col, valid := n.Name.(*ast.ColumnRef); valid {
					call := &ast.FuncCallExpr{FuncName: &ast.ObjectName{Loc: col.Loc}}
					s.resolveInstanceCall(src, call, receiver, emit, emitAmbiguous)
				}
			} else if name, ok := n.Name.(*ast.ColumnRef); ok && !src.shadowedProcedureCalls[n] {
				call := &ast.FuncCallExpr{FuncName: &ast.ObjectName{Loc: name.Loc}}
				s.resolveCall(src, call, schema, currentPackage, emit, emitAmbiguous)
			}
		}
		return true
	})
}
