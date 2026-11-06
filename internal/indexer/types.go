package indexer

import (
	"strings"

	"github.com/bytebase/omni/oracle/ast"
	"github.com/sourcegraph/scip/bindings/go/scip"
)

// reference emits a local or global occurrence once per source position.
func (v *localResolver) reference(a, b int, symbol string, role scip.SymbolRole) {
	if symbol == "" || b <= a || v.occurrenceAt(a) != nil {
		return
	}
	o := &scip.Occurrence{Range: v.src.rng(a, b), Symbol: symbol, SymbolRoles: int32(role)}
	v.src.doc.Occurrences = append(v.src.doc.Occurrences, o)
	line, col := v.src.position(a)
	v.occurrences[[2]int32{line, col}] = o
}
func (v *localResolver) linkType(symbol, target string) {
	if symbol == "" || target == "" {
		return
	}
	for _, info := range v.src.doc.Symbols {
		if info.Symbol != symbol {
			continue
		}
		present := false
		for _, r := range info.Relationships {
			if r.Symbol == target && r.IsTypeDefinition {
				present = true
				break
			}
		}
		if !present {
			info.Relationships = append(info.Relationships, &scip.Relationship{Symbol: target, IsTypeDefinition: true})
		}
	}
}

// anchoredType resolves %TYPE and %ROWTYPE using the immutable catalog. For
// row types and object types it returns the key whose fields can be resolved.
func (v *localResolver) anchoredType(t *ast.TypeName, variable string) string {
	if t == nil {
		return ""
	}
	parts := nameParts(v.src.text, &ast.ObjectName{Loc: t.Loc})
	if len(parts) == 0 {
		return ""
	}
	if t.IsPercType && len(parts) == 1 {
		if target := v.scope.lookup(name(parts[0].raw)); target != "" && target != variable {
			v.reference(parts[0].start, parts[0].end, target, scip.SymbolRole_ReadAccess)
			v.linkType(variable, target)
		}
		return ""
	}
	tableIndex := 0
	schema := v.schema
	explicit := false
	if t.IsPercType {
		if len(parts) == 3 {
			schema = parts[0].raw
			tableIndex = 1
			explicit = true
		} else if len(parts) != 2 {
			return ""
		}
	} else {
		if len(parts) == 2 {
			schema = parts[0].raw
			tableIndex = 1
			explicit = true
		} else if len(parts) != 1 {
			return ""
		}
	}
	object := parts[tableIndex]
	key, symbol := v.state.resolveKey(schema, object.raw, !explicit)
	if key == "" {
		return ""
	}
	kind := v.state.objectKinds[key]
	if t.IsPercType || t.IsPercRowtype {
		if kind != scip.SymbolInformation_Struct {
			return ""
		}
	} else if kind != scip.SymbolInformation_Class {
		return ""
	}
	v.reference(object.start, object.end, symbol, scip.SymbolRole_ReadAccess)
	if t.IsPercType {
		field := parts[len(parts)-1]
		column := v.state.objects[key+"."+name(field.raw)]
		if column != "" {
			v.reference(field.start, field.end, column, scip.SymbolRole_ReadAccess)
			v.linkType(variable, column)
		}
		return ""
	}
	v.linkType(variable, symbol)
	return key
}
func (v *localResolver) rowField(col *ast.ColumnRef) {
	parts := nameParts(v.src.text, &ast.ObjectName{Loc: col.Loc})
	if len(parts) != 2 {
		return
	}
	root := parts[0]
	field := parts[1]
	rowSymbol, key := v.scope.row(name(root.raw))
	if rowSymbol == "" || key == "" {
		return
	}
	symbol := v.state.typeField(key, name(field.raw))
	if symbol == "" {
		return
	}
	v.reference(root.start, root.end, rowSymbol, scip.SymbolRole_ReadAccess)
	role := v.accessRole()
	v.reference(field.start, field.end, symbol, role)
}
func (v *localResolver) triggerField(bind *ast.BindVariable) {
	if bind.Member == "" || v.triggerTable == "" {
		return
	}
	text := v.src.text
	pos := bind.Loc.Start
	if pos < 0 || pos >= len(text) || text[pos] != ':' {
		return
	}
	raw, _, end := tokenAt(text, pos+1)
	if !v.triggerAliases[name(raw)] {
		return
	}
	for end < len(text) && strings.ContainsRune(" \t\r\n", rune(text[end])) {
		end++
	}
	if end >= len(text) || text[end] != '.' {
		return
	}
	field, a, b := tokenAt(text, end+1)
	if field == "" {
		return
	}
	symbol := v.state.objects[v.triggerTable+"."+name(field)]
	role := v.accessRole()
	v.reference(a, b, symbol, role)
}
func (s *localScope) row(n string) (string, string) {
	for ; s != nil; s = s.parent {
		if symbol := s.bindings[n]; symbol != "" {
			return symbol, s.rowTypes[n]
		}
	}
	return "", ""
}

// collectPackageRowTypes runs after the repository and external catalog have
// been merged, so package-body resolution does not depend on file order.
func (s *state) collectPackageRowTypes() {
	for _, src := range s.sources {
		for _, u := range src.units {
			pkg, ok := u.node.(*ast.CreatePackageStmt)
			if !ok || pkg.Name == nil || pkg.Body == nil {
				continue
			}
			schema := effectiveSchema(src, u)
			raw, _, _ := objectToken(src.text, pkg.Name)
			pkgKey := name(schema) + "." + name(raw)
			for _, item := range pkg.Body.Items {
				decl, ok := item.(*ast.PLSQLVarDecl)
				if !ok || decl.TypeName == nil || decl.TypeName.IsPercType {
					continue
				}
				parts := nameParts(src.text, &ast.ObjectName{Loc: decl.TypeName.Loc})
				typeSchema := schema
				explicit := false
				switch len(parts) {
				case 1:
				case 2:
					typeSchema = parts[0].raw
					parts = parts[1:]
					explicit = true
				default:
					continue
				}
				key, _ := s.resolveKey(typeSchema, parts[0].raw, !explicit)
				kind := s.objectKinds[key]
				if decl.TypeName.IsPercRowtype && kind != scip.SymbolInformation_Struct {
					continue
				}
				if !decl.TypeName.IsPercRowtype && kind != scip.SymbolInformation_Class {
					continue
				}
				member, _, _ := tokenAt(src.text, decl.Loc.Start)
				if member == "" {
					continue
				}
				if s.packageRowTypes[pkgKey] == nil {
					s.packageRowTypes[pkgKey] = map[string]string{}
				}
				s.packageRowTypes[pkgKey][name(member)] = key
			}
		}
	}
}

// collectTypeParents creates a cycle-safe inheritance graph after the global
// catalog is complete. Both local and snapshot-defined supertypes may be used.
func (s *state) collectTypeParents() {
	for _, src := range s.sources {
		for _, u := range src.units {
			n, ok := u.node.(*ast.CreateTypeStmt)
			if !ok || n.Name == nil || n.Under == nil || n.IsBody {
				continue
			}
			schema := effectiveSchema(src, u)
			raw, _, _ := objectToken(src.text, n.Name)
			key, _ := s.resolveKey(schema, raw, false)
			parentSchema := schema
			if n.Under.Schema != "" {
				parentSchema, _, _ = tokenAt(src.text, n.Under.Loc.Start)
			}
			parentRaw, _, _ := objectToken(src.text, n.Under)
			parent, _ := s.resolveKey(parentSchema, parentRaw, n.Under.Schema == "")
			if key != "" && parent != "" && key != parent && s.objectKinds[parent] == scip.SymbolInformation_Class {
				s.typeParents[key] = parent
			}
		}
	}
}
func (s *state) attachTypeRelationships(src *source) {
	for _, u := range src.units {
		n, ok := u.node.(*ast.CreateTypeStmt)
		if !ok || n.Name == nil || n.Under == nil || n.IsBody {
			continue
		}
		schema := effectiveSchema(src, u)
		raw, _, _ := objectToken(src.text, n.Name)
		key, symbol := s.resolveKey(schema, raw, false)
		parentKey := s.typeParents[key]
		parent := s.objects[parentKey]
		if symbol == "" || parent == "" {
			continue
		}
		for _, info := range src.doc.Symbols {
			if info.Symbol == symbol {
				info.Relationships = append(info.Relationships, &scip.Relationship{Symbol: parent, IsImplementation: true})
				break
			}
		}
	}
}
func (s *state) typeField(key, field string) string {
	seen := map[string]bool{}
	for key != "" && !seen[key] {
		seen[key] = true
		var symbol string
		if s.objectKinds[key] == scip.SymbolInformation_Class {
			symbol = s.typeBindings[key][field]
		} else {
			symbol = s.objects[key+"."+field]
		}
		if symbol != "" {
			return symbol
		}
		key = s.typeParents[key]
	}
	return ""
}
