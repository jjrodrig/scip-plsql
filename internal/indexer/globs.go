package indexer

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"
)

type compiledGlob []string

func compileGlobs(patterns []string) ([]compiledGlob, error) {
	compiled := make([]compiledGlob, 0, len(patterns))
	for _, pattern := range patterns {
		pattern = strings.TrimSpace(pattern)
		pattern = strings.TrimPrefix(pattern, "./")
		if pattern == "" || strings.HasPrefix(pattern, "/") || strings.Contains(pattern, "\\") {
			return nil, fmt.Errorf("patrón no válido %q: usa rutas relativas con /", pattern)
		}
		parts := strings.Split(pattern, "/")
		for _, part := range parts {
			if part == "" || part == "." || part == ".." {
				return nil, fmt.Errorf("patrón no válido %q", pattern)
			}
			if part != "**" {
				if _, err := path.Match(part, ""); err != nil {
					return nil, fmt.Errorf("patrón %q: %w", pattern, err)
				}
			}
		}
		compiled = append(compiled, compiledGlob(parts))
	}
	return compiled, nil
}

func matchesSource(relative string, includes, excludes []compiledGlob) bool {
	matched := false
	if len(includes) == 0 {
		matched = extensions[strings.ToLower(filepath.Ext(relative))]
	} else {
		for _, pattern := range includes {
			if pattern.matches(relative) {
				matched = true
				break
			}
		}
	}
	if !matched {
		return false
	}
	for _, pattern := range excludes {
		if pattern.matches(relative) {
			return false
		}
	}
	return true
}

func (pattern compiledGlob) matches(relative string) bool {
	parts := strings.Split(filepath.ToSlash(relative), "/")
	type key struct{ pattern, name int }
	memo := make(map[key]bool, (len(pattern)+1)*(len(parts)+1))
	seen := make(map[key]bool, len(memo))
	var match func(int, int) bool
	match = func(pi, ni int) bool {
		k := key{pi, ni}
		if seen[k] {
			return memo[k]
		}
		seen[k] = true
		ok := false
		switch {
		case pi == len(pattern):
			ok = ni == len(parts)
		case pattern[pi] == "**":
			ok = match(pi+1, ni) || ni < len(parts) && match(pi, ni+1)
		case ni < len(parts):
			segmentMatch, err := path.Match(pattern[pi], parts[ni])
			ok = err == nil && segmentMatch && match(pi+1, ni+1)
		}
		memo[k] = ok
		return ok
	}
	return match(0, 0)
}
