package indexer

import (
	"fmt"
	"strings"

	"github.com/bytebase/omni/oracle/ast"
	"github.com/scip-code/scip/bindings/go/scip"
)

type formal struct {
	name, typ, mode string
	optional        bool
}
type method struct {
	symbol, identity string
	params           []formal
	returnType       string
	public           bool
}

func methodParts(n ast.Node) (*ast.ObjectName, *ast.List, string) {
	switch x := n.(type) {
	case *ast.CreateProcedureStmt:
		return x.Name, x.Parameters, "P"
	case *ast.CreateFunctionStmt:
		return x.Name, x.Parameters, "F"
	}
	return nil, nil, ""
}
func parameterType(t *ast.TypeName) string {
	if t == nil {
		return ""
	}
	var parts []string
	if t.Names != nil {
		for _, n := range t.Names.Items {
			if str, ok := n.(*ast.String); ok {
				parts = append(parts, name(str.Str))
			}
		}
	}
	out := strings.Join(parts, ".")
	if t.IsPercType {
		out += "%TYPE"
	}
	if t.IsPercRowtype {
		out += "%ROWTYPE"
	}
	if t.IsRef {
		out = "REF " + out
	}
	return out
}
func parameterMode(mode string) string {
	fields := strings.Fields(strings.ToUpper(mode))
	hasIn, hasOut := false, false
	for _, field := range fields {
		if field == "IN" {
			hasIn = true
		}
		if field == "OUT" {
			hasOut = true
		}
	}
	if hasOut && hasIn {
		return "IN OUT"
	}
	if hasOut {
		return "OUT"
	}
	return "IN"
}
func methodSignature(params *ast.List, kind string) (string, []formal) {
	var formals []formal
	var types []string
	if params != nil {
		for _, n := range params.Items {
			p, ok := n.(*ast.Parameter)
			if !ok {
				continue
			}
			typ := parameterType(p.TypeName)
			mode := parameterMode(p.Mode)
			types = append(types, mode+":"+typ)
			formals = append(formals, formal{name: p.Name, typ: typ, mode: mode, optional: p.Default != nil})
		}
	}
	return kind + "(" + strings.Join(types, ",") + ")", formals
}

// collectMethods establishes spec declaration order before inspecting bodies,
// so file order and body implementation order cannot change symbol identity.
func (s *state) collectMethods() {
	// Standalone subprograms have no overloads, but their parameter modes are
	// still needed to classify argument access at call sites.
	for _, src := range s.sources {
		for _, u := range src.units {
			obj, params, kind := methodParts(u.node)
			if obj == nil {
				continue
			}
			schema := effectiveSchema(src, u)
			raw, _, _ := objectToken(src.text, obj)
			if schema == "" || raw == "" {
				continue
			}
			identity, formals := methodSignature(params, kind)
			key := name(schema) + "." + name(raw)
			s.standalone[key] = &method{symbol: s.global(schema, raw, "()."), identity: identity, params: formals, public: true}
		}
	}
	for _, body := range []bool{false, true} {
		for _, src := range s.sources {
			for _, u := range src.units {
				var owner *ast.ObjectName
				var members *ast.List
				var isBody bool
				switch n := u.node.(type) {
				case *ast.CreatePackageStmt:
					owner = n.Name
					members = n.Body
					isBody = n.IsBody
				case *ast.CreateTypeStmt:
					owner = n.Name
					isBody = n.IsBody
					if n.IsBody {
						members = n.Body
					} else {
						members = n.Methods
					}
				default:
					continue
				}
				if owner == nil || isBody != body || members == nil {
					continue
				}
				schema := u.schema
				if owner.Schema != "" {
					schema, _, _ = tokenAt(src.text, owner.Loc.Start)
				}
				if schema == "" {
					continue
				}
				raw, _, _ := objectToken(src.text, owner)
				pkgKey := name(schema) + "." + name(raw)
				pkgSym := s.global(schema, raw, "#")
				for _, entry := range members.Items {
					item := entry
					if wrapped, ok := entry.(*ast.TypeBodyMember); ok {
						item = wrapped.Subprog
					}

					obj, params, kind := methodParts(item)
					if obj == nil {
						continue
					}
					raw, _, _ := tokenAt(src.text, obj.Loc.Start)
					if raw == "" {
						continue
					}
					key := pkgKey + "." + name(raw)
					identity, formals := methodSignature(params, kind)
					var found *method
					for _, m := range s.methods[key] {
						if m.identity == identity {
							found = m
							break
						}
					}
					if found == nil {
						idx := len(s.methods[key])
						suffix := "()."
						if idx > 0 {
							suffix = fmt.Sprintf("(+%d).", idx)
						}
						found = &method{symbol: pkgSym + escape(name(raw)) + suffix, identity: identity, params: formals, public: !body}
						s.methods[key] = append(s.methods[key], found)
					} else if !body {
						found.public = true
						found.params = formals
					}
					s.methodNodes[item] = found
				}
			}
		}
	}
}

// nameParts returns source tokens (with quotes intact) and byte ranges for a
// dotted call name. The parser stores the prefix as a single Schema string.
type namePart struct {
	raw        string
	start, end int
}

func nameParts(text string, obj *ast.ObjectName) []namePart {
	if obj == nil {
		return nil
	}
	raw, a, b := tokenAt(text, obj.Loc.Start)
	if raw == "" {
		return nil
	}
	out := []namePart{{raw, a, b}}
	for {
		i := b
		for i < len(text) && strings.ContainsRune(" \t\r\n", rune(text[i])) {
			i++
		}
		if i >= len(text) || text[i] != '.' {
			break
		}
		raw, a, b = tokenAt(text, i+1)
		if raw == "" {
			break
		}
		out = append(out, namePart{raw, a, b})
		if len(out) > 3 {
			return nil
		}
	}
	return out
}
func literalClass(typ string) byte {
	switch typ {
	case "NUMBER", "INTEGER", "INT", "DECIMAL", "NUMERIC", "PLS_INTEGER", "BINARY_INTEGER", "BINARY_FLOAT", "BINARY_DOUBLE", "FLOAT":
		return 'n'
	case "CHAR", "VARCHAR", "VARCHAR2", "NCHAR", "NVARCHAR2", "STRING", "LONG":
		return 's'
	}
	return 0
}
func argumentPositions(m *method, args *ast.List) ([]int, bool) {
	var items []ast.Node
	if args != nil {
		items = args.Items
	}
	if len(items) > len(m.params) {
		return nil, false
	}
	used := make([]bool, len(m.params))
	positions := make([]int, len(items))
	pos := 0
	for j, item := range items {
		arg := item
		idx := pos
		if named, ok := item.(*ast.NamedArgExpr); ok {
			idx = -1
			for i, p := range m.params {
				if p.name == named.Name {
					idx = i
					break
				}
			}
			arg = named.Expr
		} else {
			pos++
		}
		if idx < 0 || idx >= len(used) || used[idx] {
			return nil, false
		}
		used[idx] = true
		positions[j] = idx
		want := literalClass(m.params[idx].typ)
		switch arg.(type) {
		case *ast.NumberLiteral:
			if want == 's' {
				return nil, false
			}
		case *ast.StringLiteral:
			if want == 'n' {
				return nil, false
			}
		}
	}
	for i, p := range m.params {
		if !used[i] && !p.optional {
			return nil, false
		}
	}
	return positions, true
}
func accepts(m *method, args *ast.List) bool { _, ok := argumentPositions(m, args); return ok }

func (s *state) resolveCall(src *source, call *ast.FuncCallExpr, schema, currentPackage string, emit addReference, emitAmbiguous func(int, int, []string)) {
	if call.FuncName == nil {
		return
	}
	parts := nameParts(src.text, call.FuncName)
	if len(parts) < 1 || len(parts) > 3 {
		return
	}
	if call.FuncName.DBLink != "" {
		remoteSchema := schema
		var packagePart, member namePart
		var packageSymbol, methodSymbol string
		switch {
		case len(parts) == 1:
			member = parts[0]
			methodSymbol = s.remoteGlobal(call.FuncName.DBLink, remoteSchema, member.raw, "().")
		case len(parts) == 2 && call.FuncName.Schema != "":
			remoteSchema = parts[0].raw
			member = parts[1]
			methodSymbol = s.remoteGlobal(call.FuncName.DBLink, remoteSchema, member.raw, "().")
		case len(parts) == 2:
			packagePart, member = parts[0], parts[1]
			packageSymbol = s.remoteGlobal(call.FuncName.DBLink, remoteSchema, packagePart.raw, "#")
		case len(parts) == 3:
			remoteSchema = parts[0].raw
			packagePart, member = parts[1], parts[2]
			packageSymbol = s.remoteGlobal(call.FuncName.DBLink, remoteSchema, packagePart.raw, "#")
		}
		if packageSymbol != "" {
			s.externalSymbol(packageSymbol, packagePart.raw, scip.SymbolInformation_Package, "")
			methodSymbol = packageSymbol + escape(name(member.raw)) + "()."
			s.externalSymbol(methodSymbol, member.raw, scip.SymbolInformation_Method, packageSymbol)
			emit(packagePart.start, packagePart.end, packageSymbol, scip.SymbolRole_ReadAccess)
		} else {
			s.externalSymbol(methodSymbol, member.raw, scip.SymbolInformation_Function, "")
		}
		emit(member.start, member.end, methodSymbol, scip.SymbolRole_ReadAccess)
		return
	}
	var pkgPart, member namePart
	var pkgKey, pkgSym string
	if len(parts) == 1 {
		member = parts[0]
		pkgKey = currentPackage
		if currentPackage == "" || len(s.methods[currentPackage+"."+name(member.raw)]) == 0 {
			key, symbol := s.resolveKey(schema, member.raw, true)
			if s.objectKinds[key] == scip.SymbolInformation_Function {
				emit(member.start, member.end, symbol, scip.SymbolRole_ReadAccess)
			}
			return
		}
	} else if len(parts) == 3 {
		pkgPart = parts[1]
		member = parts[2]
		pkgKey, pkgSym = s.resolveKey(parts[0].raw, pkgPart.raw, false)
	} else {
		pkgPart = parts[0]
		member = parts[1]
		pkgKey, pkgSym = s.resolveKey(schema, pkgPart.raw, true)
	}
	if pkgSym == "" && len(parts) == 2 {
		// If the first segment is not a package in the current schema, it
		// may qualify a standalone subprogram by schema.
		key, standalone := s.resolveKey(parts[0].raw, parts[1].raw, false)
		if s.objectKinds[key] == scip.SymbolInformation_Function && strings.HasSuffix(standalone, "().") {
			emit(parts[1].start, parts[1].end, standalone, scip.SymbolRole_ReadAccess)
		}
		return
	}
	if pkgSym == "" && len(parts) > 1 {
		return
	}
	if len(parts) > 1 && s.objectKinds[pkgKey] != scip.SymbolInformation_Package && s.objectKinds[pkgKey] != scip.SymbolInformation_Class {
		return
	}
	methods := s.methods[pkgKey+"."+name(member.raw)]
	if len(methods) == 0 {
		return
	}
	var candidates []string
	for _, m := range methods {
		if (m.public || pkgKey == currentPackage) && accepts(m, call.Args) {
			candidates = append(candidates, m.symbol)
		}
	}
	if len(parts) > 1 {
		emit(pkgPart.start, pkgPart.end, pkgSym, scip.SymbolRole_ReadAccess)
	}
	if len(candidates) == 1 {
		emit(member.start, member.end, candidates[0], scip.SymbolRole_ReadAccess)
	} else if len(candidates) > 1 {
		emitAmbiguous(member.start, member.end, candidates)
	}
}

type typedCall struct{ receiver, typeKey string }

// resolveInstanceCall uses the receiver's statically known object type. If a
// method is not declared directly, its nearest inherited declaration is used.
func (s *state) resolveInstanceCall(src *source, call *ast.FuncCallExpr, receiver typedCall, emit addReference, ambiguous func(int, int, []string)) {
	parts := nameParts(src.text, call.FuncName)
	if len(parts) != 2 {
		return
	}
	seen := map[string]bool{}
	var methods []*method
	for key := receiver.typeKey; key != "" && !seen[key]; key = s.typeParents[key] {
		seen[key] = true
		methods = s.methods[key+"."+name(parts[1].raw)]
		if len(methods) > 0 {
			break
		}
	}
	if len(methods) == 0 {
		return
	}
	var candidates []string
	for _, m := range methods {
		if m.public && accepts(m, call.Args) {
			candidates = append(candidates, m.symbol)
		}
	}
	if len(candidates) == 0 {
		return
	}
	emit(parts[0].start, parts[0].end, receiver.receiver, scip.SymbolRole_ReadAccess)
	if len(candidates) == 1 {
		emit(parts[1].start, parts[1].end, candidates[0], scip.SymbolRole_ReadAccess)
	} else {
		ambiguous(parts[1].start, parts[1].end, candidates)
	}
}
