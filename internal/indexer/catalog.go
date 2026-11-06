package indexer

import (
	"github.com/bytebase/omni/oracle/ast"
	"github.com/sourcegraph/scip/bindings/go/scip"
)

type synonym struct {
	target string
	remote bool
}

// resolveKey follows private and public synonyms only after checking the
// current schema. The returned key identifies the final object and its members.
// Cycles, missing targets and database links are deliberately left unresolved.
func (s *state) resolveKey(schema, object string, public bool) (string, string) {
	if schema == "" {
		return "", ""
	}
	key := name(schema) + "." + name(object)
	if _, ok := s.objects[key]; !ok && public {
		key = "PUBLIC." + name(object)
	}
	return s.resolveCanonicalKey(key)
}

func (s *state) resolveCanonicalKey(key string) (string, string) {
	seen := map[string]bool{}
	for {
		if seen[key] {
			return "", ""
		}
		seen[key] = true
		symbol := s.objects[key]
		if symbol == "" {
			return "", ""
		}
		target, ok := s.synonyms[key]
		if !ok {
			return key, symbol
		}
		if target.remote {
			return "", ""
		}
		key = target.target
	}
}

// targetKey obtains the spelling from the source so quoted object names keep
// their case. An unqualified target is in the creating session's schema.
func targetKey(src *source, target *ast.ObjectName, schema string) string {
	if target == nil {
		return ""
	}
	if target.Schema != "" {
		raw, _, _ := tokenAt(src.text, target.Loc.Start)
		schema = raw
	}
	raw, _, _ := objectToken(src.text, target)
	return name(schema) + "." + name(raw)
}

// attachSynonymRelationships runs after all declarations are available.
func (s *state) attachSynonymRelationships(src *source) {
	for _, u := range src.units {
		n, ok := u.node.(*ast.CreateSynonymStmt)
		if !ok || n.Name == nil || n.Target == nil {
			continue
		}
		schema := u.schema
		if n.Name.Schema != "" {
			schema, _, _ = tokenAt(src.text, n.Name.Loc.Start)
		}
		if n.Public {
			schema = "PUBLIC"
		}
		raw, _, _ := objectToken(src.text, n.Name)
		alias := s.global(schema, raw, "#")
		entry, exists := s.synonyms[name(schema)+"."+name(raw)]
		if !exists {
			continue
		}
		var symbol string
		if n.Target.DBLink != "" {
			targetSchema := u.schema
			if n.Target.Schema != "" {
				targetSchema, _, _ = tokenAt(src.text, n.Target.Loc.Start)
			}
			targetName, _, _ := objectToken(src.text, n.Target)
			symbol = s.remoteObject(n.Target.DBLink, targetSchema, targetName)
		} else if !entry.remote {
			_, symbol = s.resolveCanonicalKey(entry.target)
		}
		if symbol == "" {
			continue
		}
		for _, info := range src.doc.Symbols {
			if info.Symbol == alias {
				info.Relationships = append(info.Relationships, &scip.Relationship{Symbol: symbol, IsReference: true})
				break
			}
		}
	}
}
