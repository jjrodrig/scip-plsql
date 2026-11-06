// scip-diagnostics groups SCIP diagnostics without interpreting them as errors
// in the indexer: several diagnostic codes describe supported fallback paths.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/scip-code/scip/bindings/go/scip"
	"google.golang.org/protobuf/proto"
)

type example struct {
	Path string `json:"path"`
	Line int32  `json:"line"`
}
type group struct {
	Code     string    `json:"code"`
	Message  string    `json:"message"`
	Count    int       `json:"count"`
	Examples []example `json:"examples"`
}

func summarize(index *scip.Index) []group {
	groups := map[string]*group{}
	for _, document := range index.Documents {
		for _, occurrence := range document.Occurrences {
			for _, diagnostic := range occurrence.Diagnostics {
				key := diagnostic.Code + "\x00" + strings.ToLower(diagnostic.Message)
				if groups[key] == nil {
					groups[key] = &group{Code: diagnostic.Code, Message: diagnostic.Message}
				}
				g := groups[key]
				g.Count++
				if len(g.Examples) < 3 && len(occurrence.Range) >= 1 {
					g.Examples = append(g.Examples, example{document.RelativePath, occurrence.Range[0] + 1})
				}
			}
		}
	}
	result := make([]group, 0, len(groups))
	for _, g := range groups {
		result = append(result, *g)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Count != result[j].Count {
			return result[i].Count > result[j].Count
		}
		if result[i].Code != result[j].Code {
			return result[i].Code < result[j].Code
		}
		return strings.ToLower(result[i].Message) < strings.ToLower(result[j].Message)
	})
	return result
}

func main() {
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: scip-diagnostics index.scip")
		os.Exit(2)
	}
	data, err := os.ReadFile(flag.Arg(0))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	var index scip.Index
	if err := proto.Unmarshal(data, &index); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(summarize(&index)); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
