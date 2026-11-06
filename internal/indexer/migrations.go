package indexer

import (
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/bytebase/omni/oracle/ast"
	"github.com/scip-code/scip/bindings/go/scip"
)

type ddlUnit struct {
	src     *source
	unit    unit
	version []string
}

var flywayVersion = regexp.MustCompile(`(?i)^v([0-9]+(?:[._][0-9]+)*)__`)

func migrationVersion(file string) []string {
	m := flywayVersion.FindStringSubmatch(path.Base(file))
	if m == nil {
		return nil
	}
	return strings.FieldsFunc(m[1], func(r rune) bool { return r == '.' || r == '_' })
}
func compareVersion(a, b []string) int {
	for i := 0; i < len(a) || i < len(b); i++ {
		x, y := "0", "0"
		if i < len(a) {
			x = strings.TrimLeft(a[i], "0")
			if x == "" {
				x = "0"
			}
		}
		if i < len(b) {
			y = strings.TrimLeft(b[i], "0")
			if y == "" {
				y = "0"
			}
		}
		if len(x) < len(y) {
			return -1
		}
		if len(x) > len(y) {
			return 1
		}
		if x < y {
			return -1
		}
		if x > y {
			return 1
		}
	}
	return 0
}
func (s *state) tableKey(src *source, u unit, obj *ast.ObjectName) string {
	if obj == nil {
		return ""
	}
	schema := u.schema
	if obj.Schema != "" {
		schema, _, _ = tokenAt(src.text, obj.Loc.Start)
	}
	raw, _, _ := objectToken(src.text, obj)
	if schema == "" || raw == "" {
		return ""
	}
	key, _ := s.resolveKey(schema, raw, obj.Schema == "")
	if s.objectKinds[key] != scip.SymbolInformation_Struct {
		return ""
	}
	return key
}
func (s *state) ddlReference(src *source, a, b int, symbol string) {
	if symbol == "" || b <= a {
		return
	}
	src.doc.Occurrences = append(src.doc.Occurrences, &scip.Occurrence{Range: src.rng(a, b), Symbol: symbol, SymbolRoles: int32(scip.SymbolRole_WriteAccess)})
}

// alterNames extracts DROP/RENAME column tokens, including all names in a
// DROP list. The parser currently exposes only the first name of that list.
func alterNames(src *source, cmd *ast.AlterTableCmd) []namePart {
	text := src.processed
	pos := cmd.Loc.Start
	end := cmd.Loc.End
	read := func() (string, int, int) { tok, a, b := headerToken(text, pos, end); pos = b; return tok, a, b }
	first, _, _ := read()
	if strings.EqualFold(first, "DROP") {
		tok, a, b := read()
		if strings.EqualFold(tok, "COLUMN") {
			tok, a, b = read()
		}
		if tok == "(" {
			var names []namePart
			for {
				tok, a, b = read()
				if tok == "" || tok == ")" {
					break
				}
				if !validHeaderName(tok) {
					return nil
				}
				names = append(names, namePart{src.text[a:b], a, b})
				sep, _, _ := read()
				if sep == ")" {
					break
				}
				if sep != "," {
					return nil
				}
			}
			return names
		}
		if validHeaderName(tok) {
			return []namePart{{src.text[a:b], a, b}}
		}
		return nil
	}
	if strings.EqualFold(first, "RENAME") {
		tok, _, _ := read()
		if !strings.EqualFold(tok, "COLUMN") {
			return nil
		}
		old, a, b := read()
		to, _, _ := read()
		next, c, d := read()
		if !validHeaderName(old) || !strings.EqualFold(to, "TO") || !validHeaderName(next) {
			return nil
		}
		return []namePart{{src.text[a:b], a, b}, {src.text[c:d], c, d}}
	}
	return nil
}
func (s *state) replayDDL() {
	var entries []ddlUnit
	for _, src := range s.sources {
		ver := migrationVersion(src.path)
		for _, u := range src.units {
			switch u.node.(type) {
			case *ast.CreateTableStmt, *ast.AlterTableStmt:
				entries = append(entries, ddlUnit{src, u, ver})
			}
		}
	}
	sort.SliceStable(entries, func(i, j int) bool {
		a, b := entries[i], entries[j]
		if (a.version == nil) != (b.version == nil) {
			return a.version == nil
		}
		if a.version != nil {
			if c := compareVersion(a.version, b.version); c != 0 {
				return c < 0
			}
		}
		if a.src.path != b.src.path {
			return a.src.path < b.src.path
		}
		return a.unit.start < b.unit.start
	})
	// Declarations were gathered across the repository. Remove their columns
	// before replay so an earlier ALTER cannot see a later CREATE's columns.
	for _, e := range entries {
		n, ok := e.unit.node.(*ast.CreateTableStmt)
		if !ok || n.Columns == nil {
			continue
		}
		key := s.tableKey(e.src, e.unit, n.Name)
		if key == "" {
			continue
		}
		for _, item := range n.Columns.Items {
			col, ok := item.(*ast.ColumnDef)
			if !ok {
				continue
			}
			raw, _, _ := tokenAt(e.src.text, col.Loc.Start)
			if raw == "" {
				continue
			}
			member := key + "." + name(raw)
			symbol := s.objects[member]
			if s.external[symbol] == nil {
				delete(s.objects, member)
			}
		}
	}
	active := map[string]map[string]bool{}
	for _, e := range entries {
		switch n := e.unit.node.(type) {
		case *ast.CreateTableStmt:
			key := s.tableKey(e.src, e.unit, n.Name)
			if key == "" {
				continue
			}
			for member := range active[key] {
				delete(s.objects, member)
			}
			active[key] = map[string]bool{}
			if n.Columns == nil {
				continue
			}
			for _, item := range n.Columns.Items {
				col, ok := item.(*ast.ColumnDef)
				if !ok {
					continue
				}
				raw, _, _ := tokenAt(e.src.text, col.Loc.Start)
				if raw == "" {
					continue
				}
				member := key + "." + name(raw)
				symbol := s.objects[key] + escape(name(raw)) + "."
				s.objects[member] = symbol
				delete(s.external, symbol)
				active[key][member] = true
			}
		case *ast.AlterTableStmt:
			key := s.tableKey(e.src, e.unit, n.Name)
			if key == "" || n.Actions == nil {
				continue
			}
			if active[key] == nil {
				active[key] = map[string]bool{}
			}
			for _, item := range n.Actions.Items {
				cmd, ok := item.(*ast.AlterTableCmd)
				if !ok {
					continue
				}
				switch cmd.Action {
				case ast.AT_ADD_COLUMN:
					var cols []*ast.ColumnDef
					if cmd.ColumnDef != nil {
						cols = append(cols, cmd.ColumnDef)
					}
					if cmd.ColumnDefs != nil {
						for _, x := range cmd.ColumnDefs.Items {
							if col, ok := x.(*ast.ColumnDef); ok {
								cols = append(cols, col)
							}
						}
					}
					for _, col := range cols {
						raw, a, b := tokenAt(e.src.text, col.Loc.Start)
						if raw == "" {
							continue
						}
						member := key + "." + name(raw)
						symbol := s.objects[key] + escape(name(raw)) + "."
						s.objects[member] = symbol
						delete(s.external, symbol)
						active[key][member] = true
						add(e.src, decl{symbol, name(raw), a, b, int32(scip.SymbolRole_Definition), scip.SymbolInformation_Field})
					}
				case ast.AT_DROP_COLUMN:
					for _, part := range alterNames(e.src, cmd) {
						member := key + "." + name(part.raw)
						symbol := s.objects[member]
						s.ddlReference(e.src, part.start, part.end, symbol)
						delete(s.objects, member)
						delete(s.external, symbol)
						delete(active[key], member)
					}
				case ast.AT_RENAME_COLUMN:
					parts := alterNames(e.src, cmd)
					if len(parts) != 2 {
						continue
					}
					old := key + "." + name(parts[0].raw)
					oldSymbol := s.objects[old]
					if oldSymbol == "" {
						continue
					}
					s.ddlReference(e.src, parts[0].start, parts[0].end, oldSymbol)
					delete(s.objects, old)
					delete(s.external, oldSymbol)
					delete(active[key], old)
					newKey := key + "." + name(parts[1].raw)
					newSymbol := s.objects[key] + escape(name(parts[1].raw)) + "."
					s.objects[newKey] = newSymbol
					delete(s.external, newSymbol)
					active[key][newKey] = true
					add(e.src, decl{newSymbol, name(parts[1].raw), parts[1].start, parts[1].end, int32(scip.SymbolRole_Definition), scip.SymbolInformation_Field})
				}
			}
		}
	}
}
