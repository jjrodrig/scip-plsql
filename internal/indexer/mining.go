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
const miningLiteral = `(?:[0-9]+(?:\.[0-9]+)?|'(?:[^']|'')*')`
const miningFeature = `(?:` + miningQualified + `|` + miningLiteral + `)(?:\s+AS\s+` + miningIdentifier + `)?`

var miningWildcard = regexp.MustCompile(`(?i)^(PREDICTION|PREDICTION_PROBABILITY|PREDICTION_DETAILS|PREDICTION_SET|PREDICTION_COST|PREDICTION_BOUNDS|CLUSTER_ID|CLUSTER_PROBABILITY|CLUSTER_DETAILS|CLUSTER_DISTANCE|CLUSTER_SET|FEATURE_ID|FEATURE_VALUE|FEATURE_DETAILS|FEATURE_SET)\s*\(\s*((?:OF\s+ANOMALY|` + miningQualified + `))(?:\s+COST\s+MODEL)?\s*(?:,\s*(?:` + miningLiteral + `|` + miningQualified + `)){0,2}(?:\s+COST\s+MODEL)?\s+(USING)\s*(?:\*|` + miningIdentifier + `\s*\.\s*\*|` + miningFeature + `(?:\s*,\s*` + miningFeature + `)*)\s*\)`)
var miningAlias = regexp.MustCompile(`(?i)^AS\s+` + miningIdentifier)
var miningCost = regexp.MustCompile(`(?i)^COST\s+MODEL\b`)

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
		for j := modelEnd; j < usingStart; {
			if end, ok := lexicalIsland(text, j); ok {
				j = end
				continue
			}
			if cost := miningCost.FindStringIndex(text[j:usingStart]); cost != nil {
				for k := j; k < j+cost[1]; k++ {
					if out[k] != '\n' && out[k] != '\r' {
						out[k] = ' '
					}
				}
				j += cost[1]
				continue
			}
			j++
		}
		out[usingStart] = ','
		for j := usingStart + 1; j < usingEnd; j++ {
			out[j] = ' '
		}
		for j := usingEnd; j < a+m[1]; {
			if end, ok := lexicalIsland(text, j); ok {
				j = end
				continue
			}
			raw, start, end := tokenAt(text, j)
			if raw == "" {
				j++
				continue
			}
			if strings.EqualFold(raw, "AS") {
				if alias := miningAlias.FindStringIndex(text[start : a+m[1]]); alias != nil {
					for k := start; k < start+alias[1]; k++ {
						if out[k] != '\n' && out[k] != '\r' {
							out[k] = ' '
						}
					}
					j = start + alias[1]
					continue
				}
			}
			j = end
		}
		diagnostics = append(diagnostics, preprocessDiagnostic{modelStart, modelEnd, "No se resuelve el modelo de minería Oracle"})
		i = a + m[1]
	}
	return string(out), diagnostics
}
