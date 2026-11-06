package indexer

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bytebase/omni/oracle/ast"
	"github.com/sourcegraph/scip/bindings/go/scip"
	"google.golang.org/protobuf/proto"
)

// Entries are disposable. The executable fingerprint also protects against
// stale data after changes to resolution logic or parser dependencies.
const cacheVersion = "scip-plsql-doc-v4"
const cacheMagic = "PSCDOC01"

type CacheStats struct{ Hits, Misses, Invalid, Writes int }
type documentCache struct {
	dir        string
	executable [32]byte
	stats      *CacheStats
	warned     bool
}
type cachedMethod struct {
	Symbol, Identity string
	ReturnType       string
	Public           bool
	Params           []cachedFormal
}
type cachedFormal struct {
	Name, Type, Mode string
	Optional         bool
}
type cachedSynonym struct {
	Target string
	Remote bool
}

func hashPart(h hash.Hash, data []byte) {
	var n [8]byte
	size := uint64(len(data))
	for i := range n {
		n[i] = byte(size >> uint(8*i))
	}
	_, _ = h.Write(n[:])
	_, _ = h.Write(data)
}
func hashJSON(h hash.Hash, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	hashPart(h, b)
}

// catalogFingerprintForSource selects catalog entries by identifiers present
// in the source. Including same-named alternatives also handles new symbols,
// shadowing, ambiguity, and previously unresolved references conservatively.
func executableFingerprint() ([32]byte, error) {
	var result [32]byte
	path, err := os.Executable()
	if err != nil {
		return result, err
	}
	f, err := os.Open(path)
	if err != nil {
		return result, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return result, err
	}
	copy(result[:], h.Sum(nil))
	return result, nil
}
func (s *state) catalogFingerprintForSource(src *source, executable [32]byte) [32]byte {
	h := sha256.New()
	hashPart(h, []byte(cacheVersion))
	hashPart(h, executable[:])
	names := sourceIdentifiers(src)
	objects := map[string]string{}
	for key, value := range s.objects {
		if keyUsesIdentifier(key, names) {
			objects[key] = value
		}
	}
	hashJSON(h, objects)
	kinds := map[string]scip.SymbolInformation_Kind{}
	for key, value := range s.objectKinds {
		if keyUsesIdentifier(key, names) {
			kinds[key] = value
		}
	}
	hashJSON(h, kinds)
	packageBindings := selectedBindings(s.packageBindings, names)
	hashJSON(h, packageBindings)
	hashJSON(h, selectedBindings(s.packageRowTypes, names))
	hashJSON(h, selectedBindings(s.typeBindings, names))
	parents := map[string]string{}
	for key, value := range s.typeParents {
		if keyUsesIdentifier(key, names) || keyUsesIdentifier(value, names) {
			parents[key] = value
		}
	}
	hashJSON(h, parents)
	synonyms := map[string]cachedSynonym{}
	for k, v := range s.synonyms {
		if keyUsesIdentifier(k, names) || keyUsesIdentifier(v.target, names) {
			synonyms[k] = cachedSynonym{v.target, v.remote}
		}
	}
	hashJSON(h, synonyms)
	methods := map[string][]cachedMethod{}
	for k, all := range s.methods {
		if !keyUsesIdentifier(k, names) {
			continue
		}
		for _, m := range all {
			c := cachedMethod{Symbol: m.symbol, Identity: m.identity, ReturnType: m.returnType, Public: m.public}
			for _, p := range m.params {
				c.Params = append(c.Params, cachedFormal{p.name, p.typ, p.mode, p.optional})
			}
			methods[k] = append(methods[k], c)
		}
	}
	hashJSON(h, methods)
	standalone := map[string]cachedMethod{}
	for k, m := range s.standalone {
		if !keyUsesIdentifier(k, names) {
			continue
		}
		c := cachedMethod{Symbol: m.symbol, Identity: m.identity, ReturnType: m.returnType, Public: m.public}
		for _, p := range m.params {
			c.Params = append(c.Params, cachedFormal{p.name, p.typ, p.mode, p.optional})
		}
		standalone[k] = c
	}
	hashJSON(h, standalone)
	// External symbol sets are usually small and can change without a local
	// declaration, so retain their complete deterministic fingerprint.
	keys := make([]string, 0, len(s.external))
	for key := range s.external {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		b, err := proto.MarshalOptions{Deterministic: true}.Marshal(s.external[key])
		if err != nil {
			panic(err)
		}
		hashPart(h, b)
	}
	hashJSON(h, s.opts.CCFlags)
	hashPart(h, []byte(s.opts.Schema))
	hashPart(h, []byte(s.opts.SchemaPath))
	hashJSON(h, s.opts.MaxTokens)
	var result [32]byte
	copy(result[:], h.Sum(nil))
	return result
}
func sourceIdentifiers(src *source) map[string]bool {
	names := map[string]bool{}
	addWords := func(text string) {
		for i := 0; i < len(text); {
			raw, _, end := tokenAt(text, i)
			if raw != "" && end > i {
				names[name(raw)] = true
				i = end
			} else {
				i++
			}
		}
	}
	for i := 0; i < len(src.text); {
		if src.text[i] == '"' {
			raw, _, end := tokenAt(src.text, i)
			if raw != "" && end > i {
				names[name(raw)] = true
				i = end
				continue
			}
		}
		if end, ok := lexicalIsland(src.text, i); ok {
			// Parsed string literals are added from their decoded AST values
			// below; comments have no catalog effect.
			i = end
			continue
		}
		raw, _, end := tokenAt(src.text, i)
		if raw != "" && end > i {
			names[name(raw)] = true
			i = end
		} else {
			i++
		}
	}
	for _, u := range src.units {
		if u.node == nil {
			continue
		}
		ast.Inspect(u.node, func(node ast.Node) bool {
			if lit, ok := node.(*ast.StringLiteral); ok {
				addWords(lit.Val)
			}
			return true
		})
	}
	return names
}
func keyUsesIdentifier(key string, names map[string]bool) bool {
	parts := strings.Split(key, ".")
	for start := range parts {
		var combined strings.Builder
		for end := start; end < len(parts); end++ {
			if end != start {
				combined.WriteByte('.')
			}
			combined.WriteString(parts[end])
			if identifierMatches(combined.String(), names) {
				return true
			}
		}
	}
	return false
}
func identifierMatches(identifier string, names map[string]bool) bool {
	return names[identifier] || names[name(identifier)] || names[strings.ToUpper(identifier)]
}
func selectedBindings[T any](all map[string]map[string]T, names map[string]bool) map[string]map[string]T {
	selected := map[string]map[string]T{}
	for owner, members := range all {
		for member, value := range members {
			if !identifierMatches(member, names) && !keyUsesIdentifier(owner, names) {
				continue
			}
			if selected[owner] == nil {
				selected[owner] = map[string]T{}
			}
			selected[owner][member] = value
		}
	}
	return selected
}
func (c *documentCache) key(src *source, s *state) ([32]byte, error) {
	h := sha256.New()
	hashPart(h, []byte(cacheVersion))
	catalog := s.catalogFingerprintForSource(src, c.executable)
	hashPart(h, catalog[:])
	hashPart(h, []byte(src.path))
	hashPart(h, []byte(src.text))
	for _, u := range src.units {
		hashPart(h, []byte(u.schema))
	}
	// The pre-resolution document carries DDL definitions and diagnostics,
	// including their exact ranges and messages.
	partial, err := proto.MarshalOptions{Deterministic: true}.Marshal(src.doc)
	if err != nil {
		return [32]byte{}, err
	}
	hashPart(h, partial)
	var result [32]byte
	copy(result[:], h.Sum(nil))
	return result, nil
}
func (c *documentCache) filename(key [32]byte) string {
	encoded := hex.EncodeToString(key[:])
	return filepath.Join(c.dir, encoded[:2], encoded+".pb")
}
func (c *documentCache) get(key [32]byte, path string) (*scip.Document, []byte) {
	data, err := os.ReadFile(c.filename(key))
	if err != nil {
		c.stats.Misses++
		return nil, nil
	}
	if len(data) < len(cacheMagic)+sha256.Size || string(data[:len(cacheMagic)]) != cacheMagic {
		c.stats.Invalid++
		c.stats.Misses++
		return nil, nil
	}
	checksum := sha256.Sum256(data[len(cacheMagic)+sha256.Size:])
	if !bytes.Equal(checksum[:], data[len(cacheMagic):len(cacheMagic)+sha256.Size]) {
		c.stats.Invalid++
		c.stats.Misses++
		return nil, nil
	}
	payload := data[len(cacheMagic)+sha256.Size:]
	var doc scip.Document
	if proto.Unmarshal(payload, &doc) != nil || doc.RelativePath != path || doc.Language != "PLSQL" || doc.PositionEncoding != scip.PositionEncoding_UTF8CodeUnitOffsetFromLineStart {
		c.stats.Invalid++
		c.stats.Misses++
		return nil, nil
	}
	c.stats.Hits++
	return &doc, payload
}
func (c *documentCache) put(key [32]byte, payload []byte) {
	file := c.filename(key)
	if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
		c.warn(err)
		return
	}
	f, err := os.CreateTemp(filepath.Dir(file), ".tmp-*")
	if err != nil {
		c.warn(err)
		return
	}
	temp := f.Name()
	defer os.Remove(temp)
	sum := sha256.Sum256(payload)
	_, err = f.Write(append(append([]byte(cacheMagic), sum[:]...), payload...))
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(temp, file)
	}
	if err != nil {
		c.warn(err)
		return
	}
	c.stats.Writes++
}
func (c *documentCache) warn(err error) {
	if !c.warned {
		fmt.Fprintf(os.Stderr, "scip-plsql: caché no disponible: %v\n", err)
		c.warned = true
	}
}
