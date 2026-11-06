package indexer

import (
	"strings"

	"github.com/bytebase/omni/oracle/ast"
	"github.com/scip-code/scip/bindings/go/scip"
)

type dmlResolver struct {
	state          *state
	src            *source
	schema         string
	emit           addReference
	handledSelect  map[*ast.SelectStmt]bool
	handledObjects map[*ast.ObjectName]bool
}

func (d *dmlResolver) source(obj *ast.ObjectName, alias *ast.Alias, dblink string) sqlSource {
	if obj == nil {
		return sqlSource{}
	}
	d.handledObjects[obj] = true
	schema := d.schema
	if obj.Schema != "" {
		schema, _, _ = tokenAt(d.src.text, obj.Loc.Start)
	}
	raw, a, b := objectToken(d.src.text, obj)
	aliasName := name(raw)
	if alias != nil {
		aliasRaw, _, _ := tokenAt(d.src.text, alias.Loc.Start)
		if aliasRaw != "" {
			aliasName = name(aliasRaw)
		} else {
			aliasName = name(alias.Name)
		}
	}
	key, symbol := d.state.resolveKey(schema, raw, obj.Schema == "")
	if dblink == "" {
		dblink = obj.DBLink
	}
	known := symbol != "" && dblink == "" && d.state.objectKinds[key] == scip.SymbolInformation_Struct
	if dblink != "" {
		d.emit(a, b, d.state.remoteObject(dblink, schema, raw), scip.SymbolRole_ReadAccess)
	} else if known {
		d.emit(a, b, symbol, scip.SymbolRole_ReadAccess)
	}
	return sqlSource{alias: aliasName, table: name(raw), schema: name(schema), key: key, known: known}
}
func (d *dmlResolver) addSource(scope *sqlScope, expr ast.TableExpr, alias *ast.Alias) {
	if expr == nil {
		return
	}
	switch n := expr.(type) {
	case *ast.TableRef:
		if alias == nil {
			alias = n.Alias
		}
		scope.sources = append(scope.sources, d.source(n.Name, alias, n.Dblink))
	case *ast.JoinClause:
		d.addSource(scope, n.Left, nil)
		d.addSource(scope, n.Right, nil)
	case *ast.SubqueryRef:
		if alias == nil {
			alias = n.Alias
		}
		entry := sqlSource{}
		if alias != nil {
			raw, _, _ := tokenAt(d.src.text, alias.Loc.Start)
			if raw != "" {
				entry.alias = name(raw)
			} else {
				entry.alias = name(alias.Name)
			}
		}
		if sub, ok := n.Subquery.(*ast.SelectStmt); ok {
			shape := d.state.resolveSelect(d.src, sub, d.schema, nil, d.emit)
			d.handledSelect[sub] = true
			entry.columns, entry.virtual, entry.known = shape.columns, true, shape.complete
		}
		scope.sources = append(scope.sources, entry)
	case *ast.LateralRef:
		if alias == nil {
			alias = n.Alias
		}
		entry := sqlSource{}
		if alias != nil {
			raw, _, _ := tokenAt(d.src.text, alias.Loc.Start)
			if raw != "" {
				entry.alias = name(raw)
			} else {
				entry.alias = name(alias.Name)
			}
		}
		if sub, ok := n.Subquery.(*ast.SelectStmt); ok {
			shape := d.state.resolveSelect(d.src, sub, d.schema, scope, d.emit)
			d.handledSelect[sub] = true
			entry.columns, entry.virtual, entry.known = shape.columns, true, shape.complete
		}
		scope.sources = append(scope.sources, entry)
	default:
		scope.sources = append(scope.sources, sqlSource{})
	}
}
func (d *dmlResolver) read(scope *sqlScope, node ast.Node) {
	if node == nil {
		return
	}
	ast.Inspect(node, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.SubqueryRef:
			if sub, ok := v.Subquery.(*ast.SelectStmt); ok {
				if !d.handledSelect[sub] {
					d.state.resolveSelect(d.src, sub, d.schema, nil, d.emit)
					d.handledSelect[sub] = true
				}
			}
			return false
		case *ast.LateralRef:
			if sub, ok := v.Subquery.(*ast.SelectStmt); ok {
				if !d.handledSelect[sub] {
					d.state.resolveSelect(d.src, sub, d.schema, scope, d.emit)
					d.handledSelect[sub] = true
				}
			}
			return false
		case *ast.SelectStmt:
			d.state.resolveSelect(d.src, v, d.schema, scope, d.emit)
			d.handledSelect[v] = true
			return false
		case *ast.ColumnRef:
			raw, a, b := columnToken(d.src.text, v)
			if raw != "" {
				d.emit(a, b, scope.resolveReadColumn(d.state, d.src, v, raw), scip.SymbolRole_ReadAccess)
			}
			return false
		case *ast.TableRef:
			return false
		}
		return true
	})
}
func (d *dmlResolver) writeColumn(scope *sqlScope, col *ast.ColumnRef) {
	if col == nil {
		return
	}
	raw, a, b := columnToken(d.src.text, col)
	if raw != "" {
		d.emit(a, b, scope.resolveColumn(d.state, d.src, col, raw), scip.SymbolRole_WriteAccess)
	}
}
func (d *dmlResolver) setClauses(scope, target *sqlScope, list *ast.List) {
	if list == nil {
		return
	}
	for _, n := range list.Items {
		clause, ok := n.(*ast.SetClause)
		if !ok {
			continue
		}
		d.writeColumn(target, clause.Column)
		if clause.Columns != nil {
			for _, item := range clause.Columns.Items {
				if col, ok := item.(*ast.ColumnRef); ok {
					d.writeColumn(target, col)
				}
			}
		}
		if clause.Value != nil {
			d.read(scope, clause.Value)
		}
	}
}

// returningInto finds the INTO separator outside literals and comments. The
// parser stores RETURNING expressions and destinations in one list.
func returningInto(src *source, start, end int) int {
	text := src.processed
	if text == "" {
		text = src.text
	}
	if start < 0 {
		start = 0
	}
	if end > len(text) {
		end = len(text)
	}
	seen := false
	for i := start; i < end; {
		if strings.ContainsRune(" \t\r\n\f", rune(text[i])) {
			i++
			continue
		}
		if j, ok := lexicalIsland(text, i); ok {
			if j > end {
				j = end
			}
			i = j
			continue
		}
		tok, _, j := tokenAt(text, i)
		if tok == "" {
			i++
			continue
		}
		if strings.EqualFold(tok, "RETURNING") {
			seen = true
		} else if seen && strings.EqualFold(tok, "INTO") {
			return i
		}
		i = j
	}
	return -1
}
func (d *dmlResolver) returning(scope *sqlScope, list *ast.List, start, end int) {
	if list == nil {
		return
	}
	into := returningInto(d.src, start, end)
	if into < 0 {
		return
	}
	ast.Inspect(list, func(node ast.Node) bool {
		switch v := node.(type) {
		case *ast.SelectStmt:
			d.state.resolveSelect(d.src, v, d.schema, scope, d.emit)
			d.handledSelect[v] = true
			return false
		case *ast.ColumnRef:
			if v.Loc.Start < into {
				raw, a, b := columnToken(d.src.text, v)
				if raw != "" {
					d.emit(a, b, scope.resolveReadColumn(d.state, d.src, v, raw), scip.SymbolRole_ReadAccess)
				}
			}
			return false
		}
		return true
	})
}
func (d *dmlResolver) update(n *ast.UpdateStmt) {
	target := &sqlScope{}
	target.sources = append(target.sources, d.source(n.Table, n.Alias, n.Dblink))
	scope := &sqlScope{sources: append([]sqlSource{}, target.sources...)}
	if n.FromClause != nil {
		for _, part := range n.FromClause.Items {
			if expr, ok := part.(ast.TableExpr); ok {
				d.addSource(scope, expr, nil)
			} else {
				scope.sources = append(scope.sources, sqlSource{})
			}
		}
	}
	d.setClauses(scope, target, n.SetClauses)
	if n.FromClause != nil {
		d.read(scope, n.FromClause)
	}
	if n.WhereClause != nil {
		d.read(scope, n.WhereClause)
	}
	d.returning(scope, n.Returning, n.Loc.Start, n.Loc.End)
}
func (d *dmlResolver) delete(n *ast.DeleteStmt) {
	scope := &sqlScope{sources: []sqlSource{d.source(n.Table, n.Alias, n.Dblink)}}
	if n.WhereClause != nil {
		d.read(scope, n.WhereClause)
	}
	d.returning(scope, n.Returning, n.Loc.Start, n.Loc.End)
}
func (d *dmlResolver) merge(n *ast.MergeStmt) {
	target := &sqlScope{sources: []sqlSource{d.source(n.Target, n.TargetAlias, "")}}
	scope := &sqlScope{sources: append([]sqlSource{}, target.sources...)}
	d.addSource(scope, n.Source, n.SourceAlias)
	if n.Source != nil {
		d.read(scope, n.Source)
	}
	if n.On != nil {
		d.read(scope, n.On)
	}
	if n.Clauses == nil {
		return
	}
	for _, item := range n.Clauses.Items {
		c, ok := item.(*ast.MergeClause)
		if !ok {
			continue
		}
		if c.Condition != nil {
			d.read(scope, c.Condition)
		}
		d.setClauses(scope, target, c.UpdateSet)
		if c.UpdateWhere != nil {
			d.read(scope, c.UpdateWhere)
		}
		if c.DeleteWhere != nil {
			d.read(scope, c.DeleteWhere)
		}
		if c.InsertCols != nil {
			for _, col := range c.InsertCols.Items {
				if c, ok := col.(*ast.ColumnRef); ok {
					d.writeColumn(target, c)
				}
			}
		}
		if c.InsertVals != nil {
			d.read(scope, c.InsertVals)
		}
		if c.InsertWhere != nil {
			d.read(scope, c.InsertWhere)
		}
	}
}

func (d *dmlResolver) insert(n *ast.InsertStmt) {
	if n.Table != nil {
		if n.Dblink != "" || n.Table.DBLink != "" {
			d.handledObjects[n.Table] = true
			sch := d.schema
			if n.Table.Schema != "" {
				sch, _, _ = tokenAt(d.src.text, n.Table.Loc.Start)
			}
			dblink := n.Dblink
			if dblink == "" {
				dblink = n.Table.DBLink
			}
			raw, a, b := objectToken(d.src.text, n.Table)
			d.emit(a, b, d.state.remoteObject(dblink, sch, raw), scip.SymbolRole_WriteAccess)
		} else {
			d.source(n.Table, n.Alias, "")
		}
	}
	if n.Table != nil && n.Table.DBLink == "" && n.Dblink == "" && n.Columns != nil {
		schema := d.schema
		if n.Table.Schema != "" {
			schema, _, _ = tokenAt(d.src.text, n.Table.Loc.Start)
		}
		table, _ := d.state.resolveKey(schema, n.Table.Name, n.Table.Schema == "")
		for _, item := range n.Columns.Items {
			if col, ok := item.(*ast.ColumnRef); ok {
				raw, a, b := columnToken(d.src.text, col)
				d.emit(a, b, d.state.objects[table+"."+name(raw)], scip.SymbolRole_WriteAccess)
			}
		}
	}
	if n.Select != nil {
		d.read(&sqlScope{}, n.Select)
	}
	if n.Subquery != nil {
		d.read(&sqlScope{}, n.Subquery)
	}
	if n.Values != nil {
		d.read(&sqlScope{}, n.Values)
	}
	if n.ValuesRecord != nil {
		d.read(&sqlScope{}, n.ValuesRecord)
	}
	if n.Returning != nil {
		scope := &sqlScope{sources: []sqlSource{d.source(n.Table, n.Alias, n.Dblink)}}
		d.returning(scope, n.Returning, n.Loc.Start, n.Loc.End)
	}
}
