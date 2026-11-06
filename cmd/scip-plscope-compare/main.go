// scip-plscope-compare checks whether PL/Scope usages point to the same SCIP
// symbol as their declaration. It does not infer bindings for external symbols.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/jjrodrig/plsql-scip/internal/indexer"
	"github.com/scip-code/scip/bindings/go/scip"
	"google.golang.org/protobuf/proto"
)

type item struct {
	Name   string `json:"name"`
	Usage  string `json:"usage"`
	Line   int64  `json:"line"`
	Column int64  `json:"column"`
	Status string `json:"status"`
}
type report struct {
	Counts map[string]int `json:"counts"`
	Items  []item         `json:"items"`
}

func compare(doc *scip.Document, source string, rows []indexer.PLScopeIdentifier, firstLinePrefix int) report {
	lines := strings.Split(source, "\n")
	positions := make(map[[2]int32][]string)
	for _, o := range doc.Occurrences {
		if len(o.Range) >= 3 && o.Symbol != "" {
			key := [2]int32{o.Range[0], o.Range[1]}
			positions[key] = append(positions[key], o.Symbol)
		}
	}
	find := func(row indexer.PLScopeIdentifier) ([]string, bool) {
		if row.Line < 1 || row.Line > int64(len(lines)) || row.Column < 1 {
			return nil, false
		}
		line := lines[row.Line-1]
		column := int(row.Column - 1)
		if row.Line == 1 {
			column += firstLinePrefix
		}
		runes := []rune(line)
		if column >= len(runes) || !strings.HasPrefix(strings.ToUpper(string(runes[column:])), strings.ToUpper(row.Name)) {
			return nil, false
		}
		byteColumn := len(string(runes[:column]))
		return positions[[2]int32{int32(row.Line - 1), int32(byteColumn)}], true
	}
	bindings := map[string]string{}
	for _, row := range rows {
		if row.Signature == "" || row.Usage != "DECLARATION" && row.Usage != "DEFINITION" {
			continue
		}
		matches, ok := find(row)
		if ok && len(matches) == 1 {
			bindings[row.Signature] = matches[0]
		}
	}
	r := report{Counts: map[string]int{}, Items: make([]item, 0, len(rows))}
	for _, row := range rows {
		matches, ok := find(row)
		status := "unanchored"
		if !ok {
			status = "source_mismatch"
		} else if target := bindings[row.Signature]; target != "" {
			status = "missing"
			for _, match := range matches {
				if match == target {
					status = "matched"
					break
				}
			}
			if status == "missing" && len(matches) > 0 {
				status = "different_symbol"
			}
		}
		r.Counts[status]++
		r.Items = append(r.Items, item{row.Name, row.Usage, row.Line, row.Column, status})
	}
	return r
}

func main() {
	indexPath := flag.String("index", "", "SCIP index")
	rowsPath := flag.String("plscope", "", "extract --kind identifiers JSON")
	sourcePath := flag.String("source", "", "original source file")
	docPath := flag.String("document", "", "relative SCIP document path")
	prefix := flag.Int("first-line-prefix", 0, "characters stripped by Oracle from first source line")
	flag.Parse()
	if *indexPath == "" || *rowsPath == "" || *sourcePath == "" || *docPath == "" || *prefix < 0 {
		fmt.Fprintln(os.Stderr, "provide --index, --plscope, --source and --document")
		os.Exit(2)
	}
	read := func(path string) []byte {
		data, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return data
	}
	var index scip.Index
	if err := proto.Unmarshal(read(*indexPath), &index); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	var rows []indexer.PLScopeIdentifier
	if err := json.Unmarshal(read(*rowsPath), &rows); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	for _, doc := range index.Documents {
		if doc.RelativePath == *docPath {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			if err := enc.Encode(compare(doc, string(read(*sourcePath)), rows, *prefix)); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			return
		}
	}
	fmt.Fprintln(os.Stderr, "document not found:", *docPath)
	os.Exit(1)
}
