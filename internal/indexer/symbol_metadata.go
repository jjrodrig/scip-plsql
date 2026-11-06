package indexer

import (
	"strings"

	"github.com/bytebase/omni/oracle/ast"
)

type symbolMetadata struct {
	enclosingSymbol string
	enclosingStart  int
	enclosingEnd    int
	documentation   string
	signature       string
}

func setSymbolMetadata(src *source, symbol string, metadata symbolMetadata) {
	if symbol == "" {
		return
	}
	if src.symbolMetadata == nil {
		src.symbolMetadata = make(map[string]symbolMetadata)
	}
	src.symbolMetadata[symbol] = metadata
}

// declarationSignature returns the declaration header before its body or
// terminating semicolon, and the byte offsets delimiting that header.
func declarationSignature(text string, start, end int) (int, int, string) {
	if start < 0 || end > len(text) || start >= end {
		return start, start, ""
	}
	i := start
	for i < end {
		if strings.ContainsRune(" \t\r\n\f", rune(text[i])) {
			i++
			continue
		}
		if next, ok := lexicalIsland(text, i); ok && (text[i] == '-' || text[i] == '/') {
			i = next
			continue
		}
		break
	}
	headerStart := i
	depth := 0
	for i < end {
		if strings.ContainsRune(" \t\r\n\f", rune(text[i])) {
			i++
			continue
		}
		if next, ok := lexicalIsland(text, i); ok {
			i = next
			continue
		}
		word, _, next := headerToken(text, i, end)
		if next <= i {
			i++
			continue
		}
		switch strings.ToUpper(word) {
		case "(":
			depth++
		case ")":
			if depth > 0 {
				depth--
			}
		case ";":
			if depth == 0 {
				return headerStart, i, strings.TrimSpace(text[headerStart:i])
			}
		case "IS", "AS":
			if depth == 0 {
				return headerStart, i, strings.TrimSpace(text[headerStart:i])
			}
		}
		i = next
	}
	return headerStart, end, strings.TrimSpace(text[headerStart:end])
}

// leadingDocumentation returns an immediately preceding PLDoc-style comment.
func leadingDocumentation(text string, declarationStart int) string {
	if declarationStart <= 0 || declarationStart > len(text) {
		return ""
	}
	prefix := text[:declarationStart]
	trimmed := strings.TrimRight(prefix, " \t\r\n\f")
	gap := prefix[len(trimmed):]
	if strings.Count(gap, "\n") > 1 || trimmed == "" {
		return ""
	}
	if strings.HasSuffix(trimmed, "*/") {
		start := strings.LastIndex(trimmed, "/*")
		if start >= 0 && strings.HasPrefix(trimmed[start:], "/**") {
			return strings.TrimSpace(trimmed[start:])
		}
		return ""
	}
	lines := make([]string, 0, 4)
	cursor := len(trimmed)
	for cursor > 0 {
		lineStart := strings.LastIndex(trimmed[:cursor], "\n") + 1
		line := strings.TrimSpace(trimmed[lineStart:cursor])
		if !strings.HasPrefix(line, "--") {
			break
		}
		lines = append(lines, strings.TrimSpace(strings.TrimPrefix(line, "--")))
		cursor = lineStart
		if cursor == 0 {
			break
		}
		cursor--
	}
	if len(lines) == 0 {
		return ""
	}
	for left, right := 0, len(lines)-1; left < right; left, right = left+1, right-1 {
		lines[left], lines[right] = lines[right], lines[left]
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func astNodeRange(node ast.Node) (int, int, bool) {
	switch n := node.(type) {
	case *ast.CreateProcedureStmt:
		return n.Loc.Start, n.Loc.End, true
	case *ast.CreateFunctionStmt:
		return n.Loc.Start, n.Loc.End, true
	case *ast.ColumnDef:
		return n.Loc.Start, n.Loc.End, true
	case *ast.Parameter:
		return n.Loc.Start, n.Loc.End, true
	case *ast.PLSQLVarDecl:
		return n.Loc.Start, n.Loc.End, true
	case *ast.PLSQLCursorDecl:
		return n.Loc.Start, n.Loc.End, true
	case *ast.PLSQLBlock:
		return n.Loc.Start, n.Loc.End, true
	}
	return 0, 0, false
}
