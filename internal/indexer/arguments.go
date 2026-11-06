package indexer

import (
	"github.com/bytebase/omni/oracle/ast"
	"github.com/scip-code/scip/bindings/go/scip"
)

// argumentCandidates uses the same catalog and overload filter as call
// navigation. Shadowing is checked before considering schema/package names.
func (v *localResolver) argumentCandidates(call *ast.FuncCallExpr) []*method {
	if call.FuncName == nil || call.FuncName.DBLink != "" {
		return nil
	}
	parts := nameParts(v.src.text, call.FuncName)
	if len(parts) == 0 || len(parts) > 3 {
		return nil
	}
	first := name(parts[0].raw)
	var candidates []*method
	visibilityKey := ""
	if len(parts) == 2 {
		if symbol, key := v.scope.row(first); symbol != "" && key != "" {
			seen := map[string]bool{}
			for key != "" && !seen[key] {
				seen[key] = true
				candidates = v.state.methods[key+"."+name(parts[1].raw)]
				if len(candidates) > 0 {
					break
				}
				key = v.state.typeParents[key]
			}
			visibilityKey = "instance"
		} else if v.scope.lookup(first) != "" {
			return nil
		}
	}
	if len(parts) == 1 && v.scope.lookup(first) != "" {
		return nil
	}
	if len(candidates) == 0 && visibilityKey != "instance" {
		switch len(parts) {
		case 1:
			if v.owner != "" {
				candidates = v.state.methods[v.owner+"."+first]
				visibilityKey = v.owner
			}
			if len(candidates) == 0 {
				key, _ := v.state.resolveKey(v.schema, parts[0].raw, true)
				if m := v.state.standalone[key]; m != nil && v.state.objects[key] == m.symbol && v.state.objectKinds[key] == scip.SymbolInformation_Function {
					candidates = []*method{m}
					visibilityKey = key
				}
			}
		case 2:
			key, _ := v.state.resolveKey(v.schema, parts[0].raw, true)
			if key != "" {
				if v.state.objectKinds[key] != scip.SymbolInformation_Package && v.state.objectKinds[key] != scip.SymbolInformation_Class {
					return nil
				}
				candidates = v.state.methods[key+"."+name(parts[1].raw)]
				visibilityKey = key
			} else {
				key, _ = v.state.resolveKey(parts[0].raw, parts[1].raw, false)
				if m := v.state.standalone[key]; m != nil && v.state.objects[key] == m.symbol && v.state.objectKinds[key] == scip.SymbolInformation_Function {
					candidates = []*method{m}
					visibilityKey = key
				}
			}
		case 3:
			key, _ := v.state.resolveKey(parts[0].raw, parts[1].raw, false)
			if v.state.objectKinds[key] != scip.SymbolInformation_Package && v.state.objectKinds[key] != scip.SymbolInformation_Class {
				return nil
			}
			candidates = v.state.methods[key+"."+name(parts[2].raw)]
			visibilityKey = key
		}
	}
	filtered := make([]*method, 0, len(candidates))
	for _, m := range candidates {
		if (m.public || visibilityKey == v.owner) && accepts(m, call.Args) {
			filtered = append(filtered, m)
		}
	}
	return filtered
}
func (v *localResolver) argumentRoles(call *ast.FuncCallExpr) []scip.SymbolRole {
	if call.Args == nil {
		return nil
	}
	roles := make([]scip.SymbolRole, len(call.Args.Items))
	for _, candidate := range v.argumentCandidates(call) {
		positions, ok := argumentPositions(candidate, call.Args)
		if !ok {
			continue
		}
		for i, p := range positions {
			role := scip.SymbolRole_ReadAccess
			if parameterMode(candidate.params[p].mode) != "IN" {
				role = scip.SymbolRole_WriteAccess
			}
			roles[i] |= role
		}
	}
	for i := range roles {
		if roles[i] == 0 {
			roles[i] = scip.SymbolRole_ReadAccess
		}
	}
	return roles
}
