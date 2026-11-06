package indexer

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

type preprocessDiagnostic struct {
	start, end int
	message    string
}
type conditionalFrame struct {
	parent, active, selected, unknown, seenElse bool
	start                                       int
}

// preprocess keeps byte offsets and line endings unchanged. Unsupported or
// malformed conditional expressions suppress all their branches and report a
// diagnostic, instead of claiming a branch was compiled.
func preprocess(text string, flags map[string]string) (string, []preprocessDiagnostic) {
	out := []byte(text)
	var diagnostics []preprocessDiagnostic
	var stack []conditionalFrame
	flagsUpper := map[string]string{}
	keys := make([]string, 0, len(flags))
	for key := range flags {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, k := range keys {
		flagsUpper[strings.ToUpper(strings.TrimPrefix(k, "$$"))] = flags[k]
	}
	active := func() bool { return len(stack) == 0 || stack[len(stack)-1].active }
	mask := func(a, b int) {
		for j := a; j < b && j < len(out); j++ {
			if out[j] != '\n' && out[j] != '\r' {
				out[j] = ' '
			}
		}
	}
	diag := func(a, b int, msg string) { diagnostics = append(diagnostics, preprocessDiagnostic{a, b, msg}) }
	for i := 0; i < len(text); {
		// REM/REMARK lines are SQL*Plus comments. Apostrophes in them must
		// not start a PL/SQL literal spanning subsequent statements.
		if end, ok := sqlPlusRemark(text, i); ok {
			if !active() {
				mask(i, end)
			}
			i = end
			continue
		}
		// Skip lexical islands before inspecting directives or substitutions.
		if end, ok := lexicalIsland(text, i); ok {
			if !active() {
				mask(i, end)
			}
			i = end
			continue
		}
		if text[i] == '$' {
			kw, end := directiveAt(text, i)
			switch kw {
			case "IF", "ELSIF":
				thenStart, thenEnd := findThen(text, end)
				if thenStart < 0 {
					diag(i, end, "falta $THEN en la compilación condicional")
					mask(i, len(text))
					i = len(text)
					continue
				}
				parent := active()
				if kw == "ELSIF" {
					if len(stack) == 0 {
						diag(i, thenEnd, "$ELSIF sin $IF")
						mask(i, thenEnd)
						i = thenEnd
						continue
					}
					frame := &stack[len(stack)-1]
					parent = frame.parent && !frame.selected && !frame.unknown
					if frame.seenElse {
						diag(i, thenEnd, "$ELSIF después de $ELSE")
						frame.unknown = true
						parent = false
					}
				}
				value, err := false, error(nil)
				if parent {
					value, err = evalConditional(text[end:thenStart], flagsUpper)
					if err != nil {
						diag(i, thenEnd, err.Error())
					}
				}
				if kw == "IF" {
					stack = append(stack, conditionalFrame{parent: parent, active: parent && err == nil && value, selected: parent && err == nil && value, unknown: parent && err != nil, start: i})
				} else {
					frame := &stack[len(stack)-1]
					if err != nil {
						frame.unknown = true
					}
					frame.active = parent && err == nil && value
					frame.selected = frame.selected || frame.active
				}
				mask(i, thenEnd)
				i = thenEnd
				continue
			case "ELSE":
				if len(stack) == 0 {
					diag(i, end, "$ELSE sin $IF")
				} else {
					frame := &stack[len(stack)-1]
					if frame.seenElse {
						diag(i, end, "$ELSE duplicado")
						frame.unknown = true
					}
					frame.active = frame.parent && !frame.selected && !frame.unknown
					frame.selected = frame.selected || frame.active
					frame.seenElse = true
				}
				mask(i, end)
				i = end
				continue
			case "END":
				if len(stack) == 0 {
					diag(i, end, "$END sin $IF")
				} else {
					stack = stack[:len(stack)-1]
				}
				mask(i, end)
				i = end
				continue
			}
		}
		if !active() {
			mask(i, i+1)
			i++
			continue
		}
		// SQL*Plus removes a trailing hyphen before passing continued input
		// to Oracle. Keep the newline and byte positions for SCIP ranges.
		if text[i] == '-' {
			j := i + 1
			for j < len(text) && (text[j] == ' ' || text[j] == '\t' || text[j] == '\r') {
				j++
			}
			if j < len(text) && text[j] == '\n' {
				out[i] = ' '
				i++
				continue
			}
		}
		if text[i] == '&' {
			j := i + 1
			if j < len(text) && text[j] == '&' {
				j++
			}
			startName := j
			for j < len(text) && isSubName(text[j]) {
				j++
			}
			if j > startName {
				for k := i; k < startName; k++ {
					out[k] = 'x'
				}
				// SQL*Plus consumes the first dot as the variable-name
				// delimiter. A second dot remains the SQL qualifier.
				if j < len(text) && text[j] == '.' {
					out[j] = ' '
					if j+1 < len(text) && isSubName(text[j+1]) {
						out[j] = 'x'
					}
					j++
				}
				i = j
				continue
			}
		}
		i++
	}
	for _, f := range stack {
		diag(f.start, f.start+3, "$IF sin $END")
	}
	return string(out), diagnostics
}

func sqlPlusRemark(text string, i int) (int, bool) {
	if i > 0 && text[i-1] != '\n' {
		return 0, false
	}
	j := i
	for j < len(text) && (text[j] == ' ' || text[j] == '\t') {
		j++
	}
	start := j
	for j < len(text) && (text[j] >= 'A' && text[j] <= 'Z' || text[j] >= 'a' && text[j] <= 'z') {
		j++
	}
	word := strings.ToUpper(text[start:j])
	if word != "REM" && word != "REMARK" {
		return 0, false
	}
	if j < len(text) && text[j] != ' ' && text[j] != '\t' && text[j] != '\r' && text[j] != '\n' {
		return 0, false
	}
	for j < len(text) && text[j] != '\n' {
		j++
	}
	return j, true
}
func isSubName(c byte) bool {
	return c == '_' || c == '$' || c == '#' || c >= '0' && c <= '9' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z'
}
func directiveAt(text string, i int) (string, int) {
	j := i + 1
	for j < len(text) && (text[j] >= 'a' && text[j] <= 'z' || text[j] >= 'A' && text[j] <= 'Z') {
		j++
	}
	if j < len(text) && isSubName(text[j]) {
		return "", i
	}
	return strings.ToUpper(text[i+1 : j]), j
}
func findThen(text string, i int) (int, int) {
	for i < len(text) {
		if end, ok := lexicalIsland(text, i); ok {
			i = end
			continue
		}
		if text[i] == '$' {
			kw, end := directiveAt(text, i)
			if kw == "THEN" {
				return i, end
			}
		}
		i++
	}
	return -1, -1
}

// lexicalIsland skips ordinary and alternative quoted literals, quoted names,
// line comments and block comments. Unterminated islands extend to EOF.
func lexicalIsland(s string, i int) (int, bool) {
	if i >= len(s) {
		return i, false
	}
	if i+1 < len(s) && s[i] == '-' && s[i+1] == '-' {
		j := strings.IndexByte(s[i+2:], '\n')
		if j < 0 {
			return len(s), true
		}
		return i + 2 + j, true
	}
	if i+1 < len(s) && s[i] == '/' && s[i+1] == '*' {
		j := strings.Index(s[i+2:], "*/")
		if j < 0 {
			return len(s), true
		}
		return i + 2 + j + 2, true
	}
	if i+2 < len(s) && (s[i] == 'q' || s[i] == 'Q') && s[i+1] == '\'' {
		open := s[i+2]
		close := open
		switch open {
		case '[':
			close = ']'
		case '{':
			close = '}'
		case '(':
			close = ')'
		case '<':
			close = '>'
		}
		for j := i + 3; j+1 < len(s); j++ {
			if s[j] == close && s[j+1] == '\'' {
				return j + 2, true
			}
		}
		return len(s), true
	}
	if s[i] == '\'' || s[i] == '"' {
		quote := s[i]
		for j := i + 1; j < len(s); j++ {
			if s[j] == quote {
				if j+1 < len(s) && s[j+1] == quote {
					j++
					continue
				}
				return j + 1, true
			}
		}
		return len(s), true
	}
	return i, false
}

type ccValue struct {
	kind byte
	b    bool
	i    int64
}
type ccParser struct {
	tokens []string
	at     int
	flags  map[string]string
}

func ccTokens(input string) ([]string, error) {
	var out []string
	for i := 0; i < len(input); {
		c := input[i]
		if unicode.IsSpace(rune(c)) {
			i++
			continue
		}
		if end, ok := lexicalIsland(input, i); ok {
			if c == '-' || c == '/' {
				i = end
				continue
			}
			return nil, fmt.Errorf("literal condicional no soportado")
		}
		if c == '(' || c == ')' || c == '=' {
			out = append(out, string(c))
			i++
			continue
		}
		if c == '<' || c == '>' || c == '!' || c == '~' || c == '^' {
			j := i + 1
			if j < len(input) && (input[j] == '=' || c == '<' && input[j] == '>') {
				j++
			}
			out = append(out, input[i:j])
			i = j
			continue
		}
		if c == '$' && i+1 < len(input) && input[i+1] == '$' {
			j := i + 2
			for j < len(input) && isSubName(input[j]) {
				j++
			}
			if j == i+2 {
				return nil, fmt.Errorf("flag condicional inválido")
			}
			out = append(out, strings.ToUpper(input[i:j]))
			i = j
			continue
		}
		if isSubName(c) || c == '-' {
			j := i + 1
			for j < len(input) && isSubName(input[j]) {
				j++
			}
			out = append(out, strings.ToUpper(input[i:j]))
			i = j
			continue
		}
		return nil, fmt.Errorf("carácter condicional no soportado: %q", c)
	}
	return out, nil
}
func evalConditional(input string, flags map[string]string) (bool, error) {
	tokens, err := ccTokens(input)
	if err != nil {
		return false, err
	}
	p := ccParser{tokens: tokens, flags: flags}
	value, err := p.or()
	if err != nil {
		return false, err
	}
	if p.at != len(tokens) || value.kind != 'b' {
		return false, fmt.Errorf("expresión condicional inválida o no booleana")
	}
	return value.b, nil
}
func (p *ccParser) take(s string) bool {
	if p.at < len(p.tokens) && p.tokens[p.at] == s {
		p.at++
		return true
	}
	return false
}
func (p *ccParser) or() (ccValue, error) {
	v, e := p.and()
	if e != nil {
		return v, e
	}
	for p.take("OR") {
		w, e := p.and()
		if e != nil {
			return w, e
		}
		if v.kind != 'b' || w.kind != 'b' {
			return v, fmt.Errorf("OR requiere booleanos")
		}
		v.b = v.b || w.b
	}
	return v, nil
}
func (p *ccParser) and() (ccValue, error) {
	v, e := p.not()
	if e != nil {
		return v, e
	}
	for p.take("AND") {
		w, e := p.not()
		if e != nil {
			return w, e
		}
		if v.kind != 'b' || w.kind != 'b' {
			return v, fmt.Errorf("AND requiere booleanos")
		}
		v.b = v.b && w.b
	}
	return v, nil
}
func (p *ccParser) not() (ccValue, error) {
	if p.take("NOT") {
		v, e := p.not()
		if e != nil {
			return v, e
		}
		if v.kind != 'b' {
			return v, fmt.Errorf("NOT requiere un booleano")
		}
		v.b = !v.b
		return v, nil
	}
	return p.comparison()
}
func (p *ccParser) comparison() (ccValue, error) {
	v, e := p.atom()
	if e != nil {
		return v, e
	}
	if p.at >= len(p.tokens) {
		return v, nil
	}
	op := p.tokens[p.at]
	switch op {
	case "=", "!=", "<>", "~=", "^=", "<", ">", "<=", ">=":
	default:
		return v, nil
	}
	p.at++
	w, e := p.atom()
	if e != nil {
		return w, e
	}
	if v.kind != w.kind {
		return v, fmt.Errorf("comparación condicional entre tipos distintos")
	}
	cmp := 0
	if v.kind == 'i' {
		if v.i < w.i {
			cmp = -1
		} else if v.i > w.i {
			cmp = 1
		}
	} else if v.kind == 'b' {
		if !v.b && w.b {
			cmp = -1
		} else if v.b && !w.b {
			cmp = 1
		}
	} else {
		return v, fmt.Errorf("comparación condicional inválida")
	}
	switch op {
	case "=":
		return ccValue{kind: 'b', b: cmp == 0}, nil
	case "!=", "<>", "~=", "^=":
		return ccValue{kind: 'b', b: cmp != 0}, nil
	case "<":
		return ccValue{kind: 'b', b: cmp < 0}, nil
	case ">":
		return ccValue{kind: 'b', b: cmp > 0}, nil
	case "<=":
		return ccValue{kind: 'b', b: cmp <= 0}, nil
	default:
		return ccValue{kind: 'b', b: cmp >= 0}, nil
	}
}
func (p *ccParser) atom() (ccValue, error) {
	if p.take("(") {
		v, e := p.or()
		if e != nil {
			return v, e
		}
		if !p.take(")") {
			return v, fmt.Errorf("falta ')' en expresión condicional")
		}
		return v, nil
	}
	if p.at >= len(p.tokens) {
		return ccValue{}, fmt.Errorf("expresión condicional incompleta")
	}
	tok := p.tokens[p.at]
	p.at++
	if strings.HasPrefix(tok, "$$") {
		value, ok := p.flags[tok[2:]]
		if !ok {
			return ccValue{}, fmt.Errorf("flag condicional no configurado: %s", tok)
		}
		tok = strings.ToUpper(value)
	}
	switch tok {
	case "TRUE":
		return ccValue{kind: 'b', b: true}, nil
	case "FALSE":
		return ccValue{kind: 'b', b: false}, nil
	}
	if n, e := strconv.ParseInt(tok, 10, 64); e == nil {
		return ccValue{kind: 'i', i: n}, nil
	}
	return ccValue{}, fmt.Errorf("valor condicional no soportado: %s", tok)
}
