package indexer

import (
	"crypto/sha256"
	"encoding/json"
	"hash"
	"sort"
	"strings"
)

// buildCacheCatalog hashes each exported catalog entry into every identifier
// that could select it. The conservative selection is the same as
// keyUsesIdentifier/identifierMatches, but work is done once per run instead
// of rescanning the whole catalog for every source document.
func (s *state) buildCacheCatalog() map[string][32]byte {
	groups := map[string]hash.Hash{}
	add := func(category, key string, value any, selectors ...string) {
		data, err := json.Marshal([]any{category, key, value})
		if err != nil {
			panic(err)
		}
		entry := sha256.Sum256(data)
		names := map[string]bool{}
		for _, selector := range selectors {
			parts := strings.Split(selector, ".")
			for start := range parts {
				for end := start + 1; end <= len(parts); end++ {
					part := strings.Join(parts[start:end], ".")
					names[part], names[name(part)], names[strings.ToUpper(part)] = true, true, true
				}
			}
		}
		for selector := range names {
			if groups[selector] == nil {
				groups[selector] = sha256.New()
			}
			hashPart(groups[selector], entry[:])
		}
	}
	for _, key := range sortedMapKeys(s.objects) {
		add("object", key, s.objects[key], key)
	}
	for _, key := range sortedMapKeys(s.objectKinds) {
		add("kind", key, s.objectKinds[key], key)
	}
	addBindings := func(category string, all map[string]map[string]string) {
		for _, owner := range sortedMapKeys(all) {
			for _, member := range sortedMapKeys(all[owner]) {
				add(category, owner+"\x00"+member, all[owner][member], owner, member)
			}
		}
	}
	addBindings("package", s.packageBindings)
	addBindings("rowtype", s.packageRowTypes)
	addBindings("type", s.typeBindings)
	for _, key := range sortedMapKeys(s.typeParents) {
		add("parent", key, s.typeParents[key], key, s.typeParents[key])
	}
	for _, key := range sortedMapKeys(s.synonyms) {
		v := s.synonyms[key]
		add("synonym", key, cachedSynonym{v.target, v.remote}, key, v.target)
	}
	toCached := func(m *method) cachedMethod {
		v := cachedMethod{Symbol: m.symbol, Identity: m.identity, ReturnType: m.returnType, Public: m.public}
		for _, p := range m.params {
			v.Params = append(v.Params, cachedFormal{p.name, p.typ, p.mode, p.optional})
		}
		return v
	}
	for _, key := range sortedMapKeys(s.methods) {
		var values []cachedMethod
		for _, m := range s.methods[key] {
			values = append(values, toCached(m))
		}
		add("methods", key, values, key)
	}
	for _, key := range sortedMapKeys(s.standalone) {
		add("standalone", key, toCached(s.standalone[key]), key)
	}
	result := make(map[string][32]byte, len(groups))
	for key, h := range groups {
		var digest [32]byte
		copy(digest[:], h.Sum(nil))
		result[key] = digest
	}
	return result
}

func sortedMapKeys[T any](m map[string]T) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
