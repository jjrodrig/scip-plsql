package indexer

import (
	"fmt"
	"strings"

	"github.com/bytebase/omni/oracle/ast"
	"github.com/sourcegraph/scip/bindings/go/scip"
)

type projection struct {
	columns  map[string]string
	complete bool
}
type cteBinding struct {
	symbol string
	shape  projection
}
type sqlSource struct {
	alias, table, schema, key string
	known, virtual            bool
	columns                   map[string]string
}
type sqlScope struct {
	parent  *sqlScope
	sources []sqlSource
	ctes    map[string]*cteBinding
	aliases map[string]string
}
type addReference func(int, int, string, scip.SymbolRole)

// columnToken identifies the last component without treating whitespace around
// dots as part of an identifier. The AST locations are UTF-8 byte offsets.
func columnToken(text string, col *ast.ColumnRef) (string, int, int) {
	raw, a, b := tokenAt(text, col.Loc.Start)
	components := 0
	if col.Schema != "" {
		components++
	}
	if col.Table != "" {
		components++
	}
	for i := 0; i < components; i++ {
		j := b
		for j < len(text) && strings.ContainsRune(" \t\r\n", rune(text[j])) {
			j++
		}
		if j >= len(text) || text[j] != '.' {
			return "", 0, 0
		}
		raw, a, b = tokenAt(text, j+1)
	}
	return raw, a, b
}
func (scope *sqlScope) findCTE(n string) *cteBinding {
	for p := scope; p != nil; p = p.parent {
		if c := p.ctes[n]; c != nil {
			return c
		}
	}
	return nil
}

// lookupColumn reports safeAbsence only if no matching or unknown SQL source
// can capture an unqualified name; callers may then try a PL/SQL binding.
func (scope *sqlScope) lookupColumn(s *state, src *source, col *ast.ColumnRef, raw string) (symbol string, safeAbsence bool) {
	qualifier, qualifierSchema := name(col.Table), name(col.Schema)
	parts := nameParts(src.text, &ast.ObjectName{Loc: col.Loc})
	if col.Table != "" && len(parts) >= 2 {
		qualifier = name(parts[len(parts)-2].raw)
	}
	if col.Schema != "" && len(parts) >= 3 {
		qualifierSchema = name(parts[len(parts)-3].raw)
	}
	for p := scope; p != nil; p = p.parent {
		if col.Table != "" {
			for _, source := range p.sources {
				if qualifier != source.alias {
					continue
				}
				if col.Schema != "" && qualifierSchema != source.schema {
					return "", false
				}
				if !source.known {
					return "", false
				}
				if source.virtual {
					return source.columns[name(raw)], false
				}
				return s.objects[source.key+"."+name(raw)], false
			}
			continue
		}
		matches := 0
		unknown := false
		for _, source := range p.sources {
			if !source.known {
				unknown = true
				continue
			}
			found := ""
			if source.virtual {
				found = source.columns[name(raw)]
			} else {
				found = s.objects[source.key+"."+name(raw)]
			}
			if found != "" {
				matches++
				symbol = found
			}
		}
		if unknown || matches > 1 {
			return "", false
		}
		if matches == 1 {
			return symbol, false
		}
	}
	return "", col.Table == "" && col.Schema == ""
}
func (scope *sqlScope) resolveReadColumn(s *state, src *source, col *ast.ColumnRef, raw string) string {
	symbol, absent := scope.lookupColumn(s, src, col, raw)
	if absent {
		return src.sqlBindings[col]
	}
	return symbol
}
func (scope *sqlScope) resolveColumn(s *state, src *source, col *ast.ColumnRef, raw string) string {
	symbol, _ := scope.lookupColumn(s, src, col, raw)
	return symbol
}

func localDefinition(src *source, raw string, a, b int) string {
	if raw == "" || a < 0 || b <= a {
		return ""
	}
	sym := fmt.Sprintf("local %d", src.nextLocal)
	src.nextLocal++
	add(src, decl{sym, name(raw), a, b, int32(scip.SymbolRole_Definition), scip.SymbolInformation_Variable})
	return sym
}

func (s *state) resolveSelect(src *source, sel *ast.SelectStmt, schema string, parent *sqlScope, emit addReference) projection {
	scope := &sqlScope{parent: parent, ctes: map[string]*cteBinding{}, aliases: map[string]string{}}
	if sel.WithClause != nil && sel.WithClause.CTEs != nil {
		for _, node := range sel.WithClause.CTEs.Items {
			cte, ok := node.(*ast.CTE)
			if !ok {
				continue
			}
			raw, a, b := tokenAt(src.text, cte.Loc.Start)
			if raw == "" {
				raw = cte.Name
			}
			binding := &cteBinding{symbol: localDefinition(src, raw, a, b)}
			cteName := name(raw)
			if cteName == "" {
				cteName = name(cte.Name)
			}
			// Register before resolving the body to permit recursive references.
			scope.ctes[cteName] = binding
			cteParent := &sqlScope{parent: parent, ctes: scope.ctes}
			shape := projection{columns: map[string]string{}}
			if child, ok := cte.Query.(*ast.SelectStmt); ok {
				shape = s.resolveSelect(src, child, schema, cteParent, emit)
			}
			if explicit := cteColumnNames(src.text, cte); len(explicit) != 0 {
				aliased := projection{columns: map[string]string{}, complete: shape.complete && len(shape.columns) == len(explicit)}
				for _, col := range explicit {
					csym := localDefinition(src, col.raw, col.start, col.end)
					if aliased.complete {
						aliased.columns[name(col.raw)] = csym
					}
				}
				shape = aliased
			}
			binding.shape = shape
		}
	}

	if sel.FromClause != nil {
		ast.Inspect(sel.FromClause, func(node ast.Node) bool {
			switch n := node.(type) {
			case *ast.SubqueryRef:
				alias, aliasRaw := "", ""
				if n.Alias != nil {
					aliasRaw, _, _ = tokenAt(src.text, n.Alias.Loc.Start)
					alias = name(aliasRaw)
				}
				shape := projection{columns: map[string]string{}}
				if sub, ok := n.Subquery.(*ast.SelectStmt); ok {
					shape = s.resolveSelect(src, sub, schema, parent, emit)
				}
				scope.sources = append(scope.sources, sqlSource{alias: alias, columns: shape.columns, virtual: true, known: shape.complete})
				return false
			case *ast.LateralRef:
				alias := ""
				if n.Alias != nil {
					raw, _, _ := tokenAt(src.text, n.Alias.Loc.Start)
					alias = name(raw)
				}
				shape := projection{columns: map[string]string{}}
				if sub, ok := n.Subquery.(*ast.SelectStmt); ok {
					shape = s.resolveSelect(src, sub, schema, scope, emit)
				}
				scope.sources = append(scope.sources, sqlSource{alias: alias, columns: shape.columns, virtual: true, known: shape.complete})
				return false
			case *ast.SelectStmt:
				return false
			case *ast.TableRef:
				if n.Name == nil {
					return false
				}
				sch := schema
				if n.Name.Schema != "" {
					sch, _, _ = tokenAt(src.text, n.Name.Loc.Start)
				}
				raw, a, b := objectToken(src.text, n.Name)
				table := name(raw)
				alias := table
				if n.Alias != nil {
					aliasRaw, _, _ := tokenAt(src.text, n.Alias.Loc.Start)
					if aliasRaw != "" {
						alias = name(aliasRaw)
					} else {
						alias = name(n.Alias.Name)
					}
				}
				if n.Name.Schema == "" {
					if cte := scope.findCTE(table); cte != nil {
						scope.sources = append(scope.sources, sqlSource{alias: alias, table: table, virtual: true, known: cte.shape.complete, columns: cte.shape.columns})
						emit(a, b, cte.symbol, scip.SymbolRole_ReadAccess)
						return false
					}
				}
				key, symbol := s.resolveKey(sch, raw, n.Name.Schema == "")
				dblink := n.Dblink
				if dblink == "" {
					dblink = n.Name.DBLink
				}
				known := symbol != "" && s.objectKinds[key] == scip.SymbolInformation_Struct && dblink == ""
				scope.sources = append(scope.sources, sqlSource{alias: alias, table: table, schema: name(sch), key: key, known: known})
				if dblink != "" {
					emit(a, b, s.remoteObject(dblink, sch, raw), scip.SymbolRole_ReadAccess)
				} else if known {
					emit(a, b, symbol, scip.SymbolRole_ReadAccess)
				}
				return false
			}
			return true
		})
	}

	// Set-operation SELECTs do not have a stable target list in this AST.
	shape := projection{columns: map[string]string{}, complete: sel.Op == ast.SETOP_NONE && sel.TargetList != nil}
	if sel.Op != ast.SETOP_NONE {
		shape.complete = false
	}
	if sel.TargetList != nil {
		for _, item := range sel.TargetList.Items {
			target, ok := item.(*ast.ResTarget)
			if !ok {
				shape.complete = false
				continue
			}
			col, direct := target.Expr.(*ast.ColumnRef)
			columnSymbol := ""
			if direct {
				raw, _, _ := columnToken(src.text, col)
				columnSymbol = scope.resolveReadColumn(s, src, col, raw)
			}
			outName := ""
			if target.Name != "" {
				outName = name(target.Name)
			}
			if outName == "" && direct {
				raw, _, _ := columnToken(src.text, col)
				outName = name(raw)
			}
			if outName == "" || strings.HasSuffix(outName, "*") {
				shape.complete = false
				continue
			}
			if target.Name != "" {
				raw, a, b := targetAliasToken(src.text, target)
				aliasSymbol := localDefinition(src, raw, a, b)
				columnSymbol = aliasSymbol
				if _, duplicate := scope.aliases[outName]; duplicate {
					scope.aliases[outName] = ""
				} else {
					scope.aliases[outName] = aliasSymbol
				}
			}
			if !direct && target.Name == "" {
				shape.complete = false
			}
			if _, exists := shape.columns[outName]; exists {
				shape.columns[outName] = ""
				shape.complete = false
			} else {
				shape.columns[outName] = columnSymbol
			}
		}
	}

	// Resolve only nodes owned by this SELECT; nested selects were handled while
	// constructing their source shapes above.
	if sel.OrderBy != nil {
		ast.Inspect(sel.OrderBy, func(node ast.Node) bool {
			col, ok := node.(*ast.ColumnRef)
			if !ok {
				return true
			}
			raw, a, b := columnToken(src.text, col)
			if raw == "" {
				return false
			}
			symbol := ""
			if col.Table == "" && col.Schema == "" {
				symbol = scope.aliases[name(raw)]
			}
			if symbol == "" {
				symbol = scope.resolveReadColumn(s, src, col, raw)
			}
			emit(a, b, symbol, scip.SymbolRole_ReadAccess)
			return false
		})
	}
	ast.Inspect(sel, func(node ast.Node) bool {
		if sel.IntoVars != nil && node == sel.IntoVars {
			return false
		}
		switch n := node.(type) {
		case *ast.WithClause:
			return false
		case *ast.List:
			if n == sel.OrderBy {
				return false
			}
		case *ast.SubqueryRef, *ast.LateralRef, *ast.TableRef:
			return false
		case *ast.SelectStmt:
			if n != sel {
				s.resolveSelect(src, n, schema, scope, emit)
				return false
			}
		case *ast.ColumnRef:
			raw, a, b := columnToken(src.text, n)
			if raw != "" {
				emit(a, b, scope.resolveReadColumn(s, src, n, raw), scip.SymbolRole_ReadAccess)
			}
			return false
		}
		return true
	})
	return shape
}

type cteColumn struct {
	raw        string
	start, end int
}

func cteColumnNames(text string, cte *ast.CTE) []cteColumn {
	if cte.Columns == nil || cte.Columns.Len() == 0 {
		return nil
	}
	if cte.Loc.Start < 0 || cte.Loc.Start >= len(text) {
		return nil
	}
	_, _, pos := tokenAt(text, cte.Loc.Start)
	end := cte.Loc.End
	if end > len(text) {
		end = len(text)
	}
	open, _, afterOpen := headerToken(text, pos, end)
	if open != "(" {
		return nil
	}
	pos = afterOpen
	var out []cteColumn
	for i, item := range cte.Columns.Items {
		want, ok := item.(*ast.String)
		if !ok || pos >= end {
			return nil
		}
		raw, a, b := headerToken(text, pos, end)
		if raw == "" || name(raw) != name(want.Str) {
			return nil
		}
		out = append(out, cteColumn{raw, a, b})
		pos = b
		separator, _, next := headerToken(text, pos, end)
		if i+1 < cte.Columns.Len() {
			if separator != "," {
				return nil
			}
			pos = next
		} else if separator != ")" {
			return nil
		}
	}
	return out
}

func targetAliasToken(text string, target *ast.ResTarget) (string, int, int) {
	if target == nil || target.Name == "" {
		return "", 0, 0
	}
	limit := target.Loc.End
	if limit > len(text) {
		limit = len(text)
	}
	var found string
	var start, end int
	for i := target.Loc.Start; i < limit; {
		if text[i] != '"' {
			if j, ok := lexicalIsland(text, i); ok {
				if j <= i {
					i++
				} else {
					i = j
				}
				continue
			}
		}
		raw, a, b := tokenAt(text, i)
		if raw == "" {
			i++
			continue
		}
		if name(raw) == name(target.Name) {
			found, start, end = raw, a, b
		}
		if b <= i {
			i++
		} else {
			i = b
		}
	}
	return found, start, end
}
