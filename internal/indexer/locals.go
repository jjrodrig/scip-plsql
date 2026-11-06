package indexer

import (
	"fmt"
	"strings"

	"github.com/bytebase/omni/oracle/ast"
	"github.com/scip-code/scip/bindings/go/scip"
)

// localResolver maintains one scope per block and subprogram. It deliberately
// leaves SQL statements to the SQL resolver: a SQL column can capture a PL/SQL
// variable with the same name.
type localResolver struct {
	src             *source
	state           *state
	schema          string
	triggerTable    string
	selfType        string
	owner           string
	enclosingSymbol string
	enclosingStart  int
	enclosingEnd    int
	callRole        scip.SymbolRole
	triggerAliases  map[string]bool
	scope           *localScope
	next            *int
	write           bool
	occurrences     map[[2]int32]*scip.Occurrence
}
type localScope struct {
	parent   *localScope
	bindings map[string]string
	rowTypes map[string]string
}

func (s *localScope) lookup(n string) string {
	for ; s != nil; s = s.parent {
		if sym := s.bindings[n]; sym != "" {
			return sym
		}
	}
	return ""
}
func (v *localResolver) child() *localResolver {
	return &localResolver{src: v.src, state: v.state, schema: v.schema, triggerTable: v.triggerTable, selfType: v.selfType, owner: v.owner, enclosingSymbol: v.enclosingSymbol, enclosingStart: v.enclosingStart, enclosingEnd: v.enclosingEnd, callRole: v.callRole, triggerAliases: v.triggerAliases, scope: &localScope{parent: v.scope, bindings: map[string]string{}, rowTypes: map[string]string{}}, next: v.next, occurrences: v.occurrences}
}
func (v *localResolver) definition(raw string, a, b int) string {
	sym := fmt.Sprintf("local %d", *v.next)
	*v.next++
	setSymbolMetadata(v.src, sym, symbolMetadata{
		enclosingSymbol: v.enclosingSymbol, enclosingStart: v.enclosingStart, enclosingEnd: v.enclosingEnd,
		documentation: leadingDocumentation(v.src.text, a),
	})
	add(v.src, decl{sym, name(raw), a, b, int32(scip.SymbolRole_Definition), scip.SymbolInformation_Variable})
	v.scope.bindings[name(raw)] = sym
	l, c := v.src.position(a)
	v.occurrences[[2]int32{l, c}] = v.src.doc.Occurrences[len(v.src.doc.Occurrences)-1]
	return sym
}
func (v *localResolver) accessRole() scip.SymbolRole {
	if v.callRole != 0 {
		return v.callRole
	}
	if v.write {
		return scip.SymbolRole_WriteAccess
	}
	return scip.SymbolRole_ReadAccess
}
func (v *localResolver) occurrenceAt(a int) *scip.Occurrence {
	l, c := v.src.position(a)
	return v.occurrences[[2]int32{l, c}]
}
func (v *localResolver) subprogram(obj *ast.ObjectName, params *ast.List, body ast.Node) {
	child := v.child()
	parentSymbol := ""
	role := int32(scip.SymbolRole_Definition)
	if obj != nil {
		_, a, _ := objectToken(v.src.text, obj)
		if o := v.occurrenceAt(a); o != nil {
			parentSymbol = o.Symbol
			role = o.SymbolRoles
		}
	}
	if parentSymbol != "" {
		if metadata, ok := v.src.symbolMetadata[parentSymbol]; ok {
			child.enclosingSymbol = parentSymbol
			child.enclosingStart = metadata.enclosingStart
			child.enclosingEnd = metadata.enclosingEnd
		} else if start, end, ok := astNodeRange(body); ok {
			child.enclosingSymbol = parentSymbol
			child.enclosingStart, child.enclosingEnd = start, end
		}
	}
	if params != nil {
		for _, item := range params.Items {
			p, ok := item.(*ast.Parameter)
			if !ok {
				continue
			}
			raw, a, b := tokenAt(v.src.text, p.Loc.Start)
			if raw == "" {
				continue
			}
			var symbol string
			if parentSymbol != "" {
				symbol = parentSymbol + "(" + escape(name(raw)) + ")"
				setSymbolMetadata(v.src, symbol, symbolMetadata{
					enclosingSymbol: parentSymbol, enclosingStart: child.enclosingStart, enclosingEnd: child.enclosingEnd,
					documentation: leadingDocumentation(v.src.text, a),
				})
				if v.occurrenceAt(a) == nil {
					add(v.src, decl{symbol, name(raw), a, b, role, scip.SymbolInformation_Parameter})
					l, c := v.src.position(a)
					v.occurrences[[2]int32{l, c}] = v.src.doc.Occurrences[len(v.src.doc.Occurrences)-1]
				}
			} else {
				symbol = child.definition(raw, a, b)
			}
			child.scope.bindings[name(raw)] = symbol
			if key := child.anchoredType(p.TypeName, symbol); key != "" {
				child.scope.rowTypes[name(raw)] = key
			}
			if p.Default != nil {
				ast.Walk(child, p.Default)
			}
		}
	}
	if body != nil {
		ast.Walk(child, body)
	}
}
func (v *localResolver) Visit(node ast.Node) ast.Visitor {
	switch n := node.(type) {
	case nil:
		return nil
	case *ast.CreatePackageStmt:
		// Package variables have global symbols, but still introduce bindings
		// visible to the package's subprograms and initialization block.
		pkg := v.child()
		if n.Name != nil {
			schema := v.schema
			if n.Name.Schema != "" {
				schema, _, _ = tokenAt(v.src.text, n.Name.Loc.Start)
			}
			raw, _, _ := objectToken(v.src.text, n.Name)
			pkgKey := name(schema) + "." + name(raw)
			pkg.owner = pkgKey
			for member, symbol := range v.state.packageBindings[pkgKey] {
				pkg.scope.bindings[member] = symbol
				if row := v.state.packageRowTypes[pkgKey][member]; row != "" {
					pkg.scope.rowTypes[member] = row
				}
			}
		}
		if n.Body != nil {
			for _, item := range n.Body.Items {
				if x, ok := item.(*ast.PLSQLVarDecl); ok {
					raw, a, _ := tokenAt(v.src.text, x.Loc.Start)
					if o := v.occurrenceAt(a); o != nil {
						pkg.scope.bindings[name(raw)] = o.Symbol
						if key := pkg.anchoredType(x.TypeName, o.Symbol); key != "" {
							pkg.scope.rowTypes[name(raw)] = key
						}
					}
				}
			}
			for _, item := range n.Body.Items {
				switch item.(type) {
				case *ast.CreateProcedureStmt, *ast.CreateFunctionStmt:
					ast.Walk(pkg, item)
				}
			}
		}
		if n.InitBlock != nil {
			ast.Walk(pkg, n.InitBlock)
		}
		return nil
	case *ast.CreateTriggerStmt:
		child := v.child()
		child.triggerAliases = map[string]bool{"NEW": true, "OLD": true}
		if n.Referencing != nil {
			if n.Referencing.NewAlias != "" {
				delete(child.triggerAliases, "NEW")
				child.triggerAliases[name(n.Referencing.NewAlias)] = true
			}
			if n.Referencing.OldAlias != "" {
				delete(child.triggerAliases, "OLD")
				child.triggerAliases[name(n.Referencing.OldAlias)] = true
			}
		}
		if n.Table != nil {
			schema := v.schema
			if n.Table.Schema != "" {
				schema, _, _ = tokenAt(v.src.text, n.Table.Loc.Start)
			}
			raw, _, _ := objectToken(v.src.text, n.Table)
			key, _ := v.state.resolveKey(schema, raw, n.Table.Schema == "")
			if v.state.objectKinds[key] == scip.SymbolInformation_Struct {
				child.triggerTable = key
			}
		}
		if n.When != nil {
			ast.Walk(child, n.When)
		}
		if n.Body != nil {
			ast.Walk(child, n.Body)
		}
		return nil
	case *ast.CreateTypeStmt:
		obj := v.child()
		if n.Name != nil {
			schema := v.schema
			if n.Name.Schema != "" {
				schema, _, _ = tokenAt(v.src.text, n.Name.Loc.Start)
			}
			raw, _, _ := objectToken(v.src.text, n.Name)
			key := name(schema) + "." + name(raw)
			obj.selfType = key
			obj.owner = key
			seen := map[string]bool{}
			for key != "" && !seen[key] {
				seen[key] = true
				for field, symbol := range v.state.typeBindings[key] {
					if obj.scope.bindings[field] == "" {
						obj.scope.bindings[field] = symbol
					}
				}
				key = v.state.typeParents[key]
			}
		}
		if n.Methods != nil {
			ast.Walk(obj, n.Methods)
		}
		if n.Body != nil {
			ast.Walk(obj, n.Body)
		}
		return nil
	case *ast.TypeBodyMember:
		if n.Subprog == nil {
			return nil
		}
		method := v.child()
		hasBody := false
		switch sub := n.Subprog.(type) {
		case *ast.CreateProcedureStmt:
			hasBody = sub.Body != nil
		case *ast.CreateFunctionStmt:
			hasBody = sub.Body != nil
		}
		if n.Kind != ast.TYPE_BODY_STATIC && hasBody && method.selfType != "" {
			usesSelf := false
			ast.Inspect(n.Subprog, func(node ast.Node) bool {
				if col, ok := node.(*ast.ColumnRef); ok && strings.EqualFold(col.Table, "SELF") {
					usesSelf = true
				}
				if call, ok := node.(*ast.FuncCallExpr); ok && call.FuncName != nil {
					parts := nameParts(method.src.text, call.FuncName)
					if len(parts) > 1 && name(parts[0].raw) == "SELF" {
						usesSelf = true
					}
				}
				return true
			})
			if usesSelf {
				symbol := fmt.Sprintf("local %d", *method.next)
				*method.next++
				method.scope.bindings["SELF"] = symbol
				method.scope.rowTypes["SELF"] = method.selfType
				method.src.doc.Symbols = append(method.src.doc.Symbols, &scip.SymbolInformation{Symbol: symbol, DisplayName: "SELF", Kind: scip.SymbolInformation_Parameter})
			}
		}
		ast.Walk(method, n.Subprog)
		return nil
	case *ast.CreateProcedureStmt:
		v.subprogram(n.Name, n.Parameters, n.Body)
		return nil
	case *ast.CreateFunctionStmt:
		v.subprogram(n.Name, n.Parameters, n.Body)
		return nil
	case *ast.PLSQLBlock:
		return v.child()
	case *ast.PLSQLVarDecl:
		raw, a, b := tokenAt(v.src.text, n.Loc.Start)
		if raw != "" {
			symbol := v.definition(raw, a, b)
			if key := v.anchoredType(n.TypeName, symbol); key != "" {
				v.scope.rowTypes[name(raw)] = key
			}
		}
		return v
	case *ast.PLSQLLoop:
		// The index is visible in the body, not in the bounds or cursor query.
		for _, part := range []ast.Node{n.Condition, n.LowerBound, n.UpperBound, n.CursorQuery} {
			if part != nil {
				ast.Walk(v, part)
			}
		}
		if n.CursorArgs != nil {
			ast.Walk(v, n.CursorArgs)
		}
		if n.Iteration != nil {
			ast.Walk(v, n.Iteration)
		}
		child := v.child()
		if n.Iterator != "" {
			_, _, end := tokenAt(v.src.text, n.Loc.Start)
			raw, a, b := tokenAt(v.src.text, end)
			if name(raw) == name(n.Iterator) {
				child.definition(raw, a, b)
			}
		}
		if n.Statements != nil {
			ast.Walk(child, n.Statements)
		}
		return nil
	case *ast.PLSQLCall:
		if col, ok := n.Name.(*ast.ColumnRef); ok {
			raw, _, _ := tokenAt(v.src.text, col.Loc.Start)
			if col.Table != "" {
				if sym, key := v.scope.row(name(raw)); sym != "" && key != "" {
					v.src.typedProcedureCalls[n] = typedCall{receiver: sym, typeKey: key}
				}
			}
			if v.scope.lookup(name(raw)) != "" {
				v.src.shadowedProcedureCalls[n] = true
			}
		}
		return v
	case *ast.FuncCallExpr:
		if n.FuncName != nil {
			parts := nameParts(v.src.text, n.FuncName)
			if len(parts) == 2 {
				if sym, key := v.scope.row(name(parts[0].raw)); sym != "" && key != "" {
					v.src.typedCalls[n] = typedCall{receiver: sym, typeKey: key}
				}
			}
			if len(parts) > 0 && v.scope.lookup(name(parts[0].raw)) != "" {
				v.src.shadowedCalls[n] = true
			}
		}
		if n.FuncName != nil {
			parts := nameParts(v.src.text, n.FuncName)
			if len(parts) == 1 {
				if symbol := v.scope.lookup(name(parts[0].raw)); symbol != "" {
					v.reference(parts[0].start, parts[0].end, symbol, v.accessRole())
				}
			}
		}
		roles := v.argumentRoles(n)
		if n.Args != nil {
			for i, arg := range n.Args.Items {
				child := *v
				child.write = false
				child.callRole = roles[i]
				if named, ok := arg.(*ast.NamedArgExpr); ok {
					if named.Expr != nil {
						ast.Walk(&child, named.Expr)
					}
				} else {
					ast.Walk(&child, arg)
				}
			}
		}
		if n.OrderBy != nil {
			ast.Walk(v, n.OrderBy)
		}
		if n.KeepClause != nil {
			ast.Walk(v, n.KeepClause)
		}
		if n.Over != nil {
			ast.Walk(v, n.Over)
		}
		return nil
	case *ast.PLSQLAssign:
		target := *v
		target.write = true
		target.callRole = 0
		if n.Target != nil {
			ast.Walk(&target, n.Target)
		}
		if n.Value != nil {
			ast.Walk(v, n.Value)
		}
		return nil
	case *ast.PLSQLRaise:
		if n.Exception != "" {
			_, _, end := tokenAt(v.src.text, n.Loc.Start)
			raw, a, b := tokenAt(v.src.text, end)
			if name(raw) == name(n.Exception) {
				if symbol := v.scope.lookup(name(raw)); symbol != "" {
					v.reference(a, b, symbol, scip.SymbolRole_ReadAccess)
				}
			}
		}
		return nil
	case *ast.PLSQLExecImmediate:
		if n.SQL != nil {
			source := *v
			source.write = false
			source.callRole = scip.SymbolRole_ReadAccess
			ast.Walk(&source, n.SQL)
		}
		target := *v
		target.write = true
		target.callRole = 0
		if n.Into != nil {
			ast.Walk(&target, n.Into)
		}
		if n.ReturningInto != nil {
			ast.Walk(&target, n.ReturningInto)
		}
		if n.Using != nil {
			for i, item := range n.Using.Items {
				arg := *v
				arg.write = false
				arg.callRole = scip.SymbolRole_ReadAccess
				if i < len(n.UsingModes) && parameterMode(n.UsingModes[i]) != "IN" {
					arg.callRole = scip.SymbolRole_WriteAccess
				}
				ast.Walk(&arg, item)
			}
		}
		return nil
	case *ast.PLSQLFetch:
		target := *v
		target.write = true
		target.callRole = 0
		if n.Into != nil {
			ast.Walk(&target, n.Into)
		}
		if n.Limit != nil {
			input := *v
			input.write = false
			input.callRole = scip.SymbolRole_ReadAccess
			ast.Walk(&input, n.Limit)
		}
		return nil
	case *ast.SelectStmt:
		v.captureSQLBindings(n)
		// INTO is a PL/SQL destination, not an SQL column reference.
		if n.IntoVars != nil {
			target := *v
			target.write = true
			target.callRole = 0
			ast.Walk(&target, n.IntoVars)
		}
		return nil
	case *ast.UpdateStmt:
		v.captureSQLBindings(n)
		v.returning(n.Returning, n.Loc.Start, n.Loc.End)
		return nil
	case *ast.DeleteStmt:
		v.captureSQLBindings(n)
		v.returning(n.Returning, n.Loc.Start, n.Loc.End)
		return nil
	case *ast.InsertStmt:
		v.captureSQLBindings(n)
		v.returning(n.Returning, n.Loc.Start, n.Loc.End)
		return nil
	case *ast.MergeStmt:
		v.captureSQLBindings(n)
		return nil
	case *ast.BindVariable:
		v.triggerField(n)
		return nil
	case *ast.ColumnRef:
		if n.Table != "" || n.Schema != "" {
			v.rowField(n)
			return nil
		}
		raw, a, b := tokenAt(v.src.text, n.Loc.Start)
		sym := v.scope.lookup(name(raw))
		if sym == "" || b <= a || v.occurrenceAt(a) != nil {
			return nil
		}
		role := v.accessRole()
		v.src.doc.Occurrences = append(v.src.doc.Occurrences, &scip.Occurrence{Range: v.src.rng(a, b), Symbol: sym, SymbolRoles: int32(role)})
		l, c := v.src.position(a)
		v.occurrences[[2]int32{l, c}] = v.src.doc.Occurrences[len(v.src.doc.Occurrences)-1]
		return nil
	}
	return v
}

// Record visible PL/SQL bindings without deciding whether SQL captures them.
// The SQL resolver makes that decision after collecting its table sources.
func (v *localResolver) captureSQLBindings(node ast.Node) {
	ast.Inspect(node, func(n ast.Node) bool {
		if bind, ok := n.(*ast.BindVariable); ok {
			v.triggerField(bind)
		}
		if col, ok := n.(*ast.ColumnRef); ok && col.Table == "" && col.Schema == "" {
			raw, _, _ := tokenAt(v.src.text, col.Loc.Start)
			if symbol := v.scope.lookup(name(raw)); symbol != "" {
				v.src.sqlBindings[col] = symbol
			}
		}
		return true
	})
}

func (v *localResolver) returning(list *ast.List, start, end int) {
	if list == nil {
		return
	}
	into := returningInto(v.src, start, end)
	if into < 0 {
		return
	}
	target := *v
	target.write = true
	target.callRole = 0
	ast.Inspect(list, func(node ast.Node) bool {
		switch n := node.(type) {
		case *ast.SelectStmt:
			return false
		case *ast.ColumnRef:
			if n.Loc.Start > into {
				ast.Walk(&target, n)
			}
			return false
		}
		return true
	})
}

func (s *state) resolveLocals(src *source) {
	next := src.nextLocal
	src.sqlBindings = map[*ast.ColumnRef]string{}
	src.shadowedCalls = map[*ast.FuncCallExpr]bool{}
	src.typedCalls = map[*ast.FuncCallExpr]typedCall{}
	src.typedProcedureCalls = map[*ast.PLSQLCall]typedCall{}
	src.shadowedProcedureCalls = map[*ast.PLSQLCall]bool{}
	occurrences := make(map[[2]int32]*scip.Occurrence, len(src.doc.Occurrences))
	for _, o := range src.doc.Occurrences {
		if len(o.Range) >= 3 && o.Symbol != "" {
			occurrences[[2]int32{o.Range[0], o.Range[1]}] = o
		}
	}
	for _, u := range src.units {
		if u.node != nil {
			schema := effectiveSchema(src, u)
			v := &localResolver{src: src, state: s, schema: schema, scope: &localScope{bindings: map[string]string{}, rowTypes: map[string]string{}}, next: &next, occurrences: occurrences}
			ast.Walk(v, u.node)
		}
	}
	src.nextLocal = next
	// Newly emitted parameter and local symbol information must be deterministic.
	// The writer sorts and deduplicates these alongside other symbol records.
}
