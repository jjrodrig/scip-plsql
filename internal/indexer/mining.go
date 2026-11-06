package indexer

import (
	"regexp"
	"strings"
)

// Omni does not yet parse Oracle mining calls with USING. Replace only simple
// wildcard or column-list forms with a same-width parse surrogate. The model
// becomes a numeric literal so it cannot be mistaken for a SQL column.
const miningIdentifier = `(?:"(?:[^"]|"")*"|[A-Z_][A-Z0-9_$#]*)`
const miningQualified = miningIdentifier + `(?:\s*\.\s*` + miningIdentifier + `)?`

var miningWildcard = regexp.MustCompile(`(?i)^(PREDICTION|PREDICTION_PROBABILITY|PREDICTION_DETAILS|PREDICTION_SET|PREDICTION_COST|CLUSTER_ID|CLUSTER_PROBABILITY|CLUSTER_DETAILS|FEATURE_ID|FEATURE_VALUE|FEATURE_DETAILS)\s*\(\s*(` + miningQualified + `)\s*(?:,\s*(?:[0-9]+(?:\.[0-9]+)?|'(?:[^']|'')*'))?\s+(USING)\s*(?:\*|` + miningQualified + `(?:\s*,\s*` + miningQualified + `)*)\s*\)`)

func preprocessMining(text string) (string, []preprocessDiagnostic) {
	out := []byte(text)
	var diagnostics []preprocessDiagnostic
	for i := 0; i < len(text); {
		if end, ok := sqlPlusRemark(text, i); ok {
			i = end
			continue
		}
		if end, ok := lexicalIsland(text, i); ok {
			i = end
			continue
		}
		if strings.ContainsRune(" \t\r\n", rune(text[i])) {
			i++
			continue
		}
		raw, a, b := tokenAt(text, i)
		if raw == "" {
			i++
			continue
		}
		m := miningWildcard.FindStringSubmatchIndex(text[a:])
		if m == nil || m[0] != 0 {
			i = b
			continue
		}
		// Do not rewrite a schema-qualified user-defined function.
		prev := a - 1
		for prev >= 0 && strings.ContainsRune(" \t\r\n", rune(text[prev])) {
			prev--
		}
		if prev >= 0 && text[prev] == '.' {
			i = b
			continue
		}
		modelStart, modelEnd := a+m[4], a+m[5]
		usingStart, usingEnd := a+m[6], a+m[7]
		for j := modelStart; j < modelEnd; j++ {
			if out[j] != '\n' && out[j] != '\r' {
				out[j] = ' '
			}
		}
		out[modelStart] = '0'
		out[usingStart] = ','
		for j := usingStart + 1; j < usingEnd; j++ {
			out[j] = ' '
		}
		diagnostics = append(diagnostics, preprocessDiagnostic{modelStart, modelEnd, "No se resuelve el modelo de minería Oracle"})
		i = a + m[1]
	}
	return string(out), diagnostics
}
