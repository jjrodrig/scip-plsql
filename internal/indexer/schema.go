package indexer

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/bytebase/omni/oracle/ast"
)

func schemaPattern(pattern string) (*regexp.Regexp, error) {
	if pattern == "" {
		return nil, nil
	}
	r, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("patrón de esquema: %w", err)
	}
	if r.NumSubexp() != 1 {
		return nil, fmt.Errorf("el patrón de esquema debe tener exactamente un grupo de captura")
	}
	return r, nil
}
func pathSchema(path, defaultSchema string, pattern *regexp.Regexp) string {
	if pattern != nil {
		parts := pattern.FindStringSubmatch(path)
		if len(parts) == 2 && parts[1] != "" {
			return parts[1]
		}
	}
	return defaultSchema
}

// assignSchemas applies ALTER SESSION in source order. A script's session does
// not leak to another file: @/@@ are not expanded by this indexer yet.
func assignSchemas(src *source, initial string) {
	current := initial
	for i := range src.units {
		u := &src.units[i]
		u.schema = current
		session, ok := u.node.(*ast.AlterSessionStmt)
		if !ok && (u.err != nil || u.limitExceeded) {
			if matched, next, a, b := fallbackSessionSchema(src, *u); matched {
				current = next
				if next == "" {
					src.schemaDiagnostics = append(src.schemaDiagnostics, preprocessDiagnostic{a, b, "No se puede determinar CURRENT_SCHEMA; no se resolverán nombres sin esquema"})
				}
			}
			continue
		}
		if !ok || !strings.EqualFold(session.Action, "SET") || session.SetParams == nil {
			continue
		}
		for _, node := range session.SetParams.Items {
			param, ok := node.(*ast.SetParam)
			if !ok || !strings.EqualFold(param.Name, "CURRENT_SCHEMA") {
				continue
			}
			next := ""
			switch v := param.Value.(type) {
			case *ast.ColumnRef:
				if v.Table == "" && v.Schema == "" {
					raw, _, _ := tokenAt(src.text, v.Loc.Start)
					if validHeaderName(raw) {
						next = raw
					}
				}
			case *ast.StringLiteral:
				if v.Val != "" {
					next = dictionarySpelling(v.Val)
				}
			}
			if next == "" {
				src.schemaDiagnostics = append(src.schemaDiagnostics, preprocessDiagnostic{param.Loc.Start, param.Loc.End, "No se puede determinar CURRENT_SCHEMA; no se resolverán nombres sin esquema"})
			}
			current = next
		}
	}
}

// fallbackSessionSchema recognizes the simple SQL*Plus form even when a token
// budget or parser failure prevented AST construction. If CURRENT_SCHEMA is
// present but the value is uncertain, callers must invalidate the old schema.
func fallbackSessionSchema(src *source, u unit) (bool, string, int, int) {
	text := src.processed
	pos := u.start
	read := func() (string, int, int) { t, a, b := headerToken(text, pos, u.end); pos = b; return t, a, b }
	for _, expected := range []string{"ALTER", "SESSION", "SET", "CURRENT_SCHEMA", "="} {
		t, _, _ := read()
		if !strings.EqualFold(t, expected) {
			return false, "", 0, 0
		}
	}
	_, a, b := read()
	if a >= u.end {
		return true, "", a, a
	}
	raw, _, end := tokenAt(src.text, a)
	if validHeaderName(raw) {
		tok, _, _ := headerToken(text, b, u.end)
		if tok == "" || tok == ";" {
			return true, raw, a, end
		}
	}
	if src.text[a] == '\'' {
		end, ok := lexicalIsland(src.text, a)
		if ok && end <= u.end && end > a+1 && src.text[end-1] == '\'' {
			tail, _, _ := headerToken(text, end, u.end)
			if tail == "" || tail == ";" {
				value := strings.ReplaceAll(src.text[a+1:end-1], "''", "'")
				if value != "" {
					return true, dictionarySpelling(value), a, end
				}
			}
		}
	}
	return true, "", a, b
}

// effectiveSchema is the owner of a CREATE unit when explicitly qualified,
// otherwise the current script schema. Synonym targets are an exception:
// their unqualified names are interpreted in the creating session's schema.
func effectiveSchema(src *source, u unit) string {
	schema := u.schema
	var obj *ast.ObjectName
	switch n := u.node.(type) {
	case *ast.CreateProcedureStmt:
		obj = n.Name
	case *ast.CreateFunctionStmt:
		obj = n.Name
	case *ast.CreatePackageStmt:
		obj = n.Name
	case *ast.CreateTypeStmt:
		obj = n.Name
	case *ast.CreateTriggerStmt:
		obj = n.Name
	case *ast.CreateTableStmt:
		obj = n.Name
	case *ast.CreateViewStmt:
		obj = n.Name
	case *ast.CreateSequenceStmt:
		obj = n.Name
	}
	if obj != nil && obj.Schema != "" {
		schema, _, _ = tokenAt(src.text, obj.Loc.Start)
	}
	return schema
}
