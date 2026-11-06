// scip-n2-compare compares PL/Scope rows across multiple source documents.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/jjrodrig/scip-plsql/internal/indexer"
	"github.com/scip-code/scip/bindings/go/scip"
	"google.golang.org/protobuf/proto"
)

type entry struct {
	Document        string `json:"document"`
	Source          string `json:"source"`
	Rows            string `json:"rows"`
	ObjectType      string `json:"object_type"`
	FirstLinePrefix int    `json:"first_line_prefix"`
}
type binding struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Symbol   string `json:"symbol"`
	Document string `json:"document,omitempty"`
	Line     int64  `json:"line,omitempty"`
	Column   int64  `json:"column,omitempty"`
}
type roleExpectation struct {
	Document string `json:"document"`
	Line     int64  `json:"line"`
	Column   int64  `json:"column"`
	Name     string `json:"name"`
	Role     string `json:"role"`
}
type manualExpectation struct {
	Document       string `json:"document"`
	Line           int64  `json:"line"`
	Column         int64  `json:"column"`
	Name           string `json:"name"`
	Symbol         string `json:"symbol"`
	Role           string `json:"role"`
	TypeDefinition string `json:"type_definition,omitempty"`
}
type manifest struct {
	Entries  []entry             `json:"entries"`
	Bindings []binding           `json:"bindings"`
	Roles    []roleExpectation   `json:"roles"`
	Manual   []manualExpectation `json:"manual"`
}
type resultItem struct {
	Document   string `json:"document"`
	Name       string `json:"name"`
	Type       string `json:"type"`
	Usage      string `json:"usage"`
	Line       int64  `json:"line"`
	Column     int64  `json:"column"`
	Status     string `json:"status"`
	Role       string `json:"role"`
	RoleSource string `json:"role_source,omitempty"`
	Expected   string `json:"expected,omitempty"`
}
type report struct {
	Counts map[string]int `json:"counts"`
	Roles  map[string]int `json:"roles"`
	Items  []resultItem   `json:"items"`
	Manual []resultItem   `json:"manual,omitempty"`
}
type input struct {
	entry  entry
	source string
	rows   []indexer.PLScopeIdentifier
	doc    *scip.Document
}

func occurrences(in input, row indexer.PLScopeIdentifier) ([]*scip.Occurrence, bool) {
	lines := strings.Split(in.source, "\n")
	if row.Line < 1 || row.Line > int64(len(lines)) || row.Column < 1 {
		return nil, false
	}
	runes := []rune(lines[row.Line-1])
	col := int(row.Column - 1)
	if row.Line == 1 {
		col += in.entry.FirstLinePrefix
	}
	if col >= len(runes) || !strings.HasPrefix(strings.ToUpper(string(runes[col:])), strings.ToUpper(row.Name)) {
		return nil, false
	}
	byteCol := int32(len(string(runes[:col])))
	var found []*scip.Occurrence
	for _, o := range in.doc.Occurrences {
		if len(o.Range) >= 3 && o.Range[0] == int32(row.Line-1) && o.Range[1] == byteCol && o.Symbol != "" {
			found = append(found, o)
		}
	}
	return found, true
}

func compare(inputs []input, bindings []binding, expectedRoles []roleExpectation) report {
	anchors := map[string]string{}
	conflicts := map[string]bool{}
	add := func(signature, symbol string) {
		if signature == "" || symbol == "" {
			return
		}
		if previous := anchors[signature]; previous != "" && previous != symbol {
			conflicts[signature] = true
		}
		anchors[signature] = symbol
	}
	for _, in := range inputs {
		for _, row := range in.rows {
			if row.ObjectType != in.entry.ObjectType {
				continue
			}
			for _, b := range bindings {
				if strings.EqualFold(row.Name, b.Name) && strings.EqualFold(row.Type, b.Type) && (b.Document == "" || b.Document == in.entry.Document) && (b.Line == 0 || b.Line == row.Line) && (b.Column == 0 || b.Column == row.Column) {
					add(row.Signature, b.Symbol)
				}
			}
			if row.Usage != "DECLARATION" && row.Usage != "DEFINITION" {
				continue
			}
			found, ok := occurrences(in, row)
			if !ok {
				continue
			}
			for _, o := range found {
				if o.SymbolRoles&int32(scip.SymbolRole_Definition|scip.SymbolRole_ForwardDefinition) != 0 {
					add(row.Signature, o.Symbol)
				}
			}
		}
	}
	r := report{Counts: map[string]int{}, Roles: map[string]int{}, Items: []resultItem{}}
	for _, in := range inputs {
		for _, row := range in.rows {
			if row.ObjectType != in.entry.ObjectType {
				continue
			}
			found, ok := occurrences(in, row)
			status, role, roleSource := "unanchored", "not_checked", ""
			target := anchors[row.Signature]
			if !ok {
				status = "source_mismatch"
			} else if conflicts[row.Signature] {
				status = "anchor_conflict"
			} else if target != "" {
				status = "missing"
				var match *scip.Occurrence
				for _, o := range found {
					if o.Symbol == target {
						match = o
						status = "matched"
						break
					}
				}
				if match != nil {
					for _, o := range found {
						if o.Symbol != target {
							status = "ambiguous"
							break
						}
					}
				}
				if match == nil && len(found) > 0 {
					status = "different_symbol"
				}
				if match != nil {
					var mask scip.SymbolRole
					switch row.Usage {
					case "DECLARATION", "DEFINITION":
						mask = scip.SymbolRole_Definition | scip.SymbolRole_ForwardDefinition
					case "ASSIGNMENT":
						mask = scip.SymbolRole_WriteAccess
					case "REFERENCE", "CALL":
						if row.Type != "COLUMN" {
							mask = scip.SymbolRole_ReadAccess
						}
					}
					if mask != 0 {
						roleSource = "plscope"
					}
					for _, expected := range expectedRoles {
						if expected.Document == in.entry.Document && expected.Line == row.Line && expected.Column == row.Column && strings.EqualFold(expected.Name, row.Name) {
							switch expected.Role {
							case "read":
								mask = scip.SymbolRole_ReadAccess
							case "write":
								mask = scip.SymbolRole_WriteAccess
							default:
								mask = 0
							}
							roleSource = "fixture"
						}
					}
					// A declaration and initialization can share one source range;
					// SCIP need not encode both as separate occurrences.
					if mask != 0 && !(row.Usage == "ASSIGNMENT" && match.SymbolRoles&int32(scip.SymbolRole_Definition) != 0) {
						role = "mismatch"
						if match.SymbolRoles&int32(mask) != 0 {
							role = "matched"
						}
					}
				}
			}
			r.Counts[status]++
			r.Roles[role]++
			r.Items = append(r.Items, resultItem{in.entry.Document, row.Name, row.Type, row.Usage, row.Line, row.Column, status, role, roleSource, target})
		}
	}
	return r
}

func main() {
	indexPath := flag.String("index", "", "SCIP index")
	manifestPath := flag.String("manifest", "", "JSON input manifest")
	flag.Parse()
	if *indexPath == "" || *manifestPath == "" {
		fmt.Fprintln(os.Stderr, "provide --index and --manifest")
		os.Exit(2)
	}
	read := func(path string) []byte {
		b, e := os.ReadFile(path)
		if e != nil {
			fmt.Fprintln(os.Stderr, e)
			os.Exit(1)
		}
		return b
	}
	var m manifest
	if e := json.Unmarshal(read(*manifestPath), &m); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
	var idx scip.Index
	if e := proto.Unmarshal(read(*indexPath), &idx); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
	docs := map[string]*scip.Document{}
	for _, d := range idx.Documents {
		docs[d.RelativePath] = d
	}
	var inputs []input
	for _, e := range m.Entries {
		d := docs[e.Document]
		if d == nil {
			fmt.Fprintln(os.Stderr, "document not found:", e.Document)
			os.Exit(1)
		}
		var rows []indexer.PLScopeIdentifier
		if err := json.Unmarshal(read(e.Rows), &rows); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		inputs = append(inputs, input{e, string(read(e.Source)), rows, d})
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	result := compare(inputs, m.Bindings, m.Roles)
	for _, expectation := range m.Manual {
		status := "source_mismatch"
		for _, in := range inputs {
			if in.entry.Document != expectation.Document {
				continue
			}
			found, ok := occurrences(in, indexer.PLScopeIdentifier{Name: expectation.Name, Line: expectation.Line, Column: expectation.Column})
			if !ok {
				break
			}
			status = "missing"
			for _, o := range found {
				if o.Symbol != expectation.Symbol {
					continue
				}
				mask := scip.SymbolRole_ReadAccess
				switch expectation.Role {
				case "write":
					mask = scip.SymbolRole_WriteAccess
				case "definition":
					mask = scip.SymbolRole_Definition
				}
				status = "role_mismatch"
				if o.SymbolRoles&int32(mask) != 0 {
					status = "matched"
				}
				if status == "matched" && expectation.TypeDefinition != "" {
					status = "relation_missing"
					for _, info := range in.doc.Symbols {
						if info.Symbol == o.Symbol {
							for _, rel := range info.Relationships {
								if rel.Symbol == expectation.TypeDefinition && rel.IsTypeDefinition {
									status = "matched"
								}
							}
						}
					}
				}
				break
			}
			break
		}
		result.Manual = append(result.Manual, resultItem{Document: expectation.Document, Name: expectation.Name, Line: expectation.Line, Column: expectation.Column, Status: status, Role: expectation.Role, RoleSource: "fixture", Expected: expectation.Symbol})
	}
	if e := enc.Encode(result); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
