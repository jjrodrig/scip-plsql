package indexer

import (
	"sort"
	"strings"

	"github.com/sourcegraph/scip/bindings/go/scip"
)

type lexicalHeader struct {
	schema, object, kind string
	start, end           int
	body, wrapped        bool
	wrappedStart         int
}

// headerToken skips comments and returns identifiers or punctuation, retaining
// exact byte positions. Only a CREATE header is interpreted, never its body.
func headerToken(text string, i, end int) (string, int, int) {
	for i < end {
		if strings.ContainsRune(" \t\r\n\f", rune(text[i])) {
			i++
			continue
		}
		if i+1 < end && (text[i:i+2] == "--" || text[i:i+2] == "/*") {
			j, _ := lexicalIsland(text, i)
			if j > end {
				j = end
			}
			i = j
			continue
		}
		break
	}
	if i >= end {
		return "", end, end
	}
	raw, a, b := tokenAt(text, i)
	if raw != "" && b <= end {
		return raw, a, b
	}
	return text[i : i+1], i, i + 1
}
func parseHeader(text string, start, end int) *lexicalHeader {
	next := start
	read := func() (string, int, int) { t, a, b := headerToken(text, next, end); next = b; return t, a, b }
	match := func(w string) bool { t, _, _ := read(); return strings.EqualFold(t, w) }
	if !match("CREATE") {
		return nil
	}
	tok, a, b := read()
	if strings.EqualFold(tok, "OR") {
		if !match("REPLACE") {
			return nil
		}
		tok, a, b = read()
	}
	if strings.EqualFold(tok, "EDITIONABLE") || strings.EqualFold(tok, "NONEDITIONABLE") {
		tok, a, b = read()
	}
	kind := strings.ToUpper(tok)
	switch kind {
	case "PACKAGE", "TYPE", "PROCEDURE", "FUNCTION", "TRIGGER", "TABLE", "VIEW", "SEQUENCE":
	default:
		return nil
	}
	h := &lexicalHeader{kind: kind}
	tok, a, b = read()
	if (kind == "PACKAGE" || kind == "TYPE") && strings.EqualFold(tok, "BODY") {
		h.body = true
		tok, a, b = read()
	}
	if !validHeaderName(tok) {
		return nil
	}
	h.object, h.start, h.end = tok, a, b
	// A qualified name is parsed as separate tokens, including whitespace.
	t, _, tb := headerToken(text, next, end)
	if t == "." {
		next = tb
		obj, oa, ob := read()
		if !validHeaderName(obj) {
			return nil
		}
		h.schema = tok
		h.object = obj
		h.start = oa
		h.end = ob
	}
	t, ta, _ := headerToken(text, next, end)
	if strings.EqualFold(t, "WRAPPED") {
		h.wrapped = true
		h.wrappedStart = ta
	}
	return h
}
func validHeaderName(s string) bool {
	if s == "" {
		return false
	}
	if s[0] == '"' {
		return len(s) >= 2 && s[len(s)-1] == '"'
	}
	c := s[0]
	return c == '_' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= 128
}

// wrappedUnits masks encrypted payloads before the generic splitter sees
// them. SQL*Plus ends such a unit with a slash on its own line; base64 text
// must not be interpreted as SQL or consume subsequent statements.
func wrappedUnits(text string) (string, []unit) {
	out := []byte(text)
	var units []unit
	for i := 0; i < len(text); {
		if strings.ContainsRune(" \t\r\n\f", rune(text[i])) {
			i++
			continue
		}
		if end, ok := lexicalIsland(text, i); ok {
			i = end
			continue
		}
		tok, a, b := tokenAt(text, i)
		if tok == "" {
			i++
			continue
		}
		i = b
		if !strings.EqualFold(tok, "CREATE") {
			continue
		}
		h := parseHeader(text, a, len(text))
		if h == nil || !h.wrapped {
			continue
		}
		end := len(text)
		lineStart := h.wrappedStart
		for lineStart < len(text) {
			lineEnd := strings.IndexByte(text[lineStart:], '\n')
			if lineEnd < 0 {
				lineEnd = len(text)
			} else {
				lineEnd += lineStart
			}
			if strings.TrimSpace(text[lineStart:lineEnd]) == "/" {
				end = lineEnd
				if end < len(text) {
					end++
				}
				break
			}
			if lineEnd == len(text) {
				break
			}
			lineStart = lineEnd + 1
		}
		for j := a; j < end; j++ {
			if out[j] != '\n' && out[j] != '\r' {
				out[j] = ' '
			}
		}
		units = append(units, unit{start: a, end: end, header: h, wrapped: true})
		i = end
	}
	return string(out), units
}
func sortUnits(units []unit) {
	sort.SliceStable(units, func(i, j int) bool { return units[i].start < units[j].start })
}

func tokenBudgetExceeded(text string, start, end, limit int) bool {
	if limit <= 0 {
		return false
	}
	count := 0
	for i := start; i < end; {
		if strings.ContainsRune(" \t\r\n\f", rune(text[i])) {
			i++
			continue
		}
		if j, ok := lexicalIsland(text, i); ok {
			if j > end {
				j = end
			}
			if text[i] != '-' && text[i] != '/' {
				count++
			}
			i = j
		} else {
			_, _, j := headerToken(text, i, end)
			if j <= i {
				j = i + 1
			}
			i = j
			count++
		}
		if count > limit {
			return true
		}
	}
	return false
}

func (s *state) declareLexicalHeader(src *source, h *lexicalHeader, defaultSchema string) {
	if h == nil {
		return
	}
	schema := defaultSchema
	if h.schema != "" {
		schema = h.schema
	}
	if schema == "" {
		return
	}
	var suffix string
	var kind scip.SymbolInformation_Kind
	switch h.kind {
	case "PACKAGE":
		suffix = "#"
		kind = scip.SymbolInformation_Package
	case "TYPE":
		suffix = "#"
		kind = scip.SymbolInformation_Class
	case "TABLE", "VIEW":
		suffix = "#"
		kind = scip.SymbolInformation_Struct
	case "PROCEDURE", "FUNCTION":
		suffix = "()."
		kind = scip.SymbolInformation_Function
	case "TRIGGER":
		suffix = "."
		kind = scip.SymbolInformation_Function
	case "SEQUENCE":
		suffix = "."
		kind = scip.SymbolInformation_Variable
	default:
		return
	}
	symbol := s.global(schema, h.object, suffix)
	role := scip.SymbolRole_Definition
	if !h.body && (h.kind == "PACKAGE" || h.kind == "TYPE") {
		role = scip.SymbolRole_ForwardDefinition
	}
	add(src, decl{symbol, name(h.object), h.start, h.end, int32(role), kind})
	key := name(schema) + "." + name(h.object)
	s.objects[key] = symbol
	s.objectKinds[key] = kind
	delete(s.synonyms, key)
}

// resolveLexicalReferences indexes only schema-qualified catalog objects after
// unambiguous table-introducing SQL keywords. A degraded unit has no scope
// information, so unqualified names and remote targets cannot be resolved
// safely. Wrapped payloads are never inspected.
func (s *state) resolveLexicalReferences(src *source, u unit, emit addReference) {
	if u.wrapped || u.fallback == "" {
		return
	}
	text := u.fallback
	base := u.start
	for i := 0; i < len(text); {
		if strings.ContainsRune(" \t\r\n\f", rune(text[i])) {
			i++
			continue
		}
		if end, ok := lexicalIsland(text, i); ok {
			if end > len(text) {
				end = len(text)
			}
			i = end
			continue
		}
		word, _, end := tokenAt(text, i)
		if word == "" {
			i++
			continue
		}
		i = end
		switch strings.ToUpper(word) {
		case "FROM", "JOIN", "UPDATE":
		default:
			continue
		}
		schema, _, b := headerToken(text, end, len(text))
		if !validHeaderName(schema) {
			continue
		}
		dot, _, b2 := headerToken(text, b, len(text))
		if dot != "." {
			continue
		}
		object, a, b3 := headerToken(text, b2, len(text))
		if !validHeaderName(object) {
			continue
		}
		next, _, _ := headerToken(text, b3, len(text))
		if next == "@" {
			continue
		}
		key, symbol := s.resolveKey(schema, object, false)
		if key == "" {
			continue
		}
		if s.objectKinds[key] != scip.SymbolInformation_Struct {
			continue
		}
		emit(base+a, base+b3, symbol, scip.SymbolRole_ReadAccess)
	}
	s.resolveLexicalRemoteCalls(src, u, emit)
}

// resolveLexicalRemoteCalls preserves navigation for remote PL/SQL calls when
// the parser rejects the database-link suffix and the unit falls back to
// lexical resolution.
func (s *state) resolveLexicalRemoteCalls(src *source, u unit, emit addReference) {
	text := u.fallback
	base := u.start
	schema := effectiveSchema(src, u)
	for i := 0; i < len(text); {
		if strings.ContainsRune(" \t\r\n\f", rune(text[i])) {
			i++
			continue
		}
		if end, ok := lexicalIsland(text, i); ok {
			if end > len(text) {
				end = len(text)
			}
			i = end
			continue
		}
		first, a, b := tokenAt(text, i)
		if !validHeaderName(first) {
			i++
			continue
		}
		// Only start at the first segment, so a three-part name is not also
		// reconsidered as a two-part name on its second segment.
		prior := a - 1
		for prior >= 0 && strings.ContainsRune(" \t\r\n\f", rune(text[prior])) {
			prior--
		}
		if prior >= 0 && text[prior] == '.' {
			i = b
			continue
		}
		parts := []namePart{{raw: first, start: a, end: b}}
		cursor := b
		for len(parts) < 3 {
			for cursor < len(text) && strings.ContainsRune(" \t\r\n\f", rune(text[cursor])) {
				cursor++
			}
			if cursor >= len(text) || text[cursor] != '.' {
				break
			}
			raw, start, end := tokenAt(text, cursor+1)
			if !validHeaderName(raw) {
				break
			}
			parts = append(parts, namePart{raw: raw, start: start, end: end})
			cursor = end
		}
		for cursor < len(text) && strings.ContainsRune(" \t\r\n\f", rune(text[cursor])) {
			cursor++
		}
		if cursor >= len(text) || text[cursor] != '@' {
			i = b
			continue
		}
		dblink, _, linkEnd := tokenAt(text, cursor+1)
		if !validHeaderName(dblink) {
			i = cursor + 1
			continue
		}
		afterLink := linkEnd
		for afterLink < len(text) && strings.ContainsRune(" \t\r\n\f", rune(text[afterLink])) {
			afterLink++
		}
		if !remoteCallContext(text, a, afterLink) {
			i = linkEnd
			continue
		}
		var pkg, member namePart
		remoteSchema := schema
		var packageSymbol string
		switch len(parts) {
		case 1:
			member = parts[0]
		case 2:
			pkg, member = parts[0], parts[1]
		case 3:
			remoteSchema, pkg, member = parts[0].raw, parts[1], parts[2]
		}
		var methodSymbol string
		if pkg.raw != "" {
			packageSymbol = s.remoteGlobal(dblink, remoteSchema, pkg.raw, "#")
			s.externalSymbol(packageSymbol, pkg.raw, scip.SymbolInformation_Package, "")
			methodSymbol = packageSymbol + escape(name(member.raw)) + "()."
			s.externalSymbol(methodSymbol, member.raw, scip.SymbolInformation_Method, packageSymbol)
			emit(base+pkg.start, base+pkg.end, packageSymbol, scip.SymbolRole_ReadAccess)
		} else {
			methodSymbol = s.remoteGlobal(dblink, remoteSchema, member.raw, "().")
			s.externalSymbol(methodSymbol, member.raw, scip.SymbolInformation_Function, "")
		}
		emit(base+member.start, base+member.end, methodSymbol, scip.SymbolRole_ReadAccess)
		i = linkEnd
	}
}

func remoteCallContext(text string, start, afterLink int) bool {
	if afterLink < len(text) && text[afterLink] == '(' {
		return true
	}
	i := start - 1
	for i >= 0 && strings.ContainsRune(" \t\r\n\f", rune(text[i])) {
		i--
	}
	if i >= 0 && strings.ContainsRune(";", rune(text[i])) {
		return true
	}
	end := i + 1
	for i >= 0 && (text[i] == '_' || text[i] == '$' || text[i] == '#' || text[i] >= '0' && text[i] <= '9' || text[i] >= 'A' && text[i] <= 'Z' || text[i] >= 'a' && text[i] <= 'z') {
		i--
	}
	return strings.EqualFold(text[i+1:end], "BEGIN") || strings.EqualFold(text[i+1:end], "THEN") || strings.EqualFold(text[i+1:end], "ELSE") || strings.EqualFold(text[i+1:end], "LOOP")
}

func retainedFallback(src *source, u *unit) string {
	if src == nil || u == nil || src.processed == "" || u.start < 0 || u.end < u.start || u.end > len(src.processed) {
		return ""
	}
	return strings.Clone(src.processed[u.start:u.end])
}
