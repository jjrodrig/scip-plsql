// Package indexer implements a first, conservative SCIP indexing pipeline.
package indexer

import (
	"encoding/hex"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/bytebase/omni/oracle/ast"
	"github.com/bytebase/omni/oracle/parser"
	"github.com/sourcegraph/scip/bindings/go/scip"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
)

type Options struct {
	Root, Output, Database, Version, Schema string
	Catalog                                 string
	SchemaPath                              string
	CCFlags                                 map[string]string
	MaxTokens                               int
	Workers                                 int
	CacheDir                                string
	CacheStats                              *CacheStats
	Metrics                                 *Metrics
	LegacyEncoding                          string
	IncludeGlobs                            []string
	ExcludeGlobs                            []string
}
type unit struct {
	start, end    int
	schema        string
	node          ast.Node
	err           error
	header        *lexicalHeader
	fallback      string
	wrapped       bool
	limitExceeded bool
}
type source struct {
	path, text              string
	processed               string
	embeddedText            bool
	units                   []unit
	doc                     *scip.Document
	symbolMetadata          map[string]symbolMetadata
	lineStarts              []int
	offsetMap               []int
	preprocessorDiagnostics []preprocessDiagnostic
	miningDiagnostics       []preprocessDiagnostic
	schemaDiagnostics       []preprocessDiagnostic
	encodingDiagnostics     []preprocessDiagnostic
	shadowedCalls           map[*ast.FuncCallExpr]bool
	shadowedProcedureCalls  map[*ast.PLSQLCall]bool
	typedCalls              map[*ast.FuncCallExpr]typedCall
	typedProcedureCalls     map[*ast.PLSQLCall]typedCall
	sqlBindings             map[*ast.ColumnRef]string
	nextLocal               int
}
type decl struct {
	symbol, name string
	start, end   int
	role         int32
	kind         scip.SymbolInformation_Kind
}
type state struct {
	opts                           Options
	sources                        []*source
	objects                        map[string]string
	objectKinds                    map[string]scip.SymbolInformation_Kind
	synonyms                       map[string]synonym
	methods                        map[string][]*method
	standalone                     map[string]*method
	methodNodes                    map[ast.Node]*method
	external                       map[string]*scip.SymbolInformation
	externalUsed                   map[string]*scip.SymbolInformation
	packageBindings                map[string]map[string]string
	packageRowTypes                map[string]map[string]string
	typeBindings                   map[string]map[string]string
	typeParents                    map[string]string
	cache                          *documentCache
	seenUTF8, seenUTF16, seenOther bool
}

var extensions = map[string]bool{".sql": true, ".pks": true, ".pkb": true, ".pls": true, ".plb": true, ".prc": true, ".fnc": true, ".trg": true, ".typ": true, ".tps": true, ".tpb": true, ".vw": true}

func Index(o Options) error {
	started := time.Now()
	if o.Metrics != nil {
		*o.Metrics = Metrics{}
		sampler := startHeapSampler()
		defer func() {
			o.Metrics.PeakHeapBytes = sampler.close()
			o.Metrics.Elapsed = time.Since(started)
		}()
	}
	root, err := filepath.Abs(o.Root)
	if err != nil {
		return err
	}
	o.Root = root
	if o.MaxTokens < 0 {
		return fmt.Errorf("el máximo de tokens no puede ser negativo")
	}
	if o.Workers < 0 {
		return fmt.Errorf("el número de trabajadores no puede ser negativo")
	}
	for _, v := range []string{o.Database, o.Version, o.Schema} {
		if v == "" || strings.ContainsAny(v, " \t\r\n") {
			return fmt.Errorf("database, package-version y schema deben ser valores no vacíos sin espacios")
		}
	}
	cacheDir := ""
	if o.CacheStats != nil {
		*o.CacheStats = CacheStats{}
	}
	if o.CacheDir != "" {
		cacheDir = o.CacheDir
		if !filepath.IsAbs(cacheDir) {
			cacheDir = filepath.Join(root, cacheDir)
		}
		cacheDir = filepath.Clean(cacheDir)
		if cacheDir == root {
			return fmt.Errorf("el directorio de caché no puede ser la raíz del proyecto")
		}
	}
	legacy, err := normalizeLegacyEncoding(o.LegacyEncoding)
	if err != nil {
		return err
	}
	pattern, err := schemaPattern(o.SchemaPath)
	if err != nil {
		return err
	}
	includeGlobs, err := compileGlobs(o.IncludeGlobs)
	if err != nil {
		return fmt.Errorf("globs de inclusión: %w", err)
	}
	excludeGlobs, err := compileGlobs(o.ExcludeGlobs)
	if err != nil {
		return fmt.Errorf("globs de exclusión: %w", err)
	}
	s := &state{opts: o, objects: map[string]string{}, objectKinds: map[string]scip.SymbolInformation_Kind{}, synonyms: map[string]synonym{}, methods: map[string][]*method{}, standalone: map[string]*method{}, methodNodes: map[ast.Node]*method{}, external: map[string]*scip.SymbolInformation{}, packageBindings: map[string]map[string]string{}, packageRowTypes: map[string]map[string]string{}, typeBindings: map[string]map[string]string{}, typeParents: map[string]string{}}
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != root && (d.Name() == ".git" || d.Name() == ".cache" || path == cacheDir) {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		rel, e := filepath.Rel(root, path)
		if e != nil {
			return e
		}
		rel = filepath.ToSlash(rel)
		if !matchesSource(rel, includeGlobs, excludeGlobs) {
			return nil
		}
		b, e := os.ReadFile(path)
		if e != nil {
			return e
		}
		decoded, e := decodeSource(b, legacy)
		if e != nil {
			fmt.Fprintf(os.Stderr, "%s: no se pudo decodificar: %v\n", path, e)
			return nil
		}
		src := &source{path: rel, text: decoded.text, embeddedText: decoded.embedded, lineStarts: []int{0}}
		if decoded.embedded {
			if offset := strings.IndexRune(decoded.text, '\uFFFD'); offset >= 0 {
				src.encodingDiagnostics = append(src.encodingDiagnostics, preprocessDiagnostic{offset, offset + len("\uFFFD"), "El texto decodificado contiene un carácter de sustitución U+FFFD"})
			}
		}
		switch decoded.encoding {
		case encodingUTF8:
			s.seenUTF8 = true
		case encodingUTF16:
			s.seenUTF16 = true
		case encodingOther:
			s.seenOther = true
		}
		for i, c := range src.text {
			if c == '\n' {
				src.lineStarts = append(src.lineStarts, i+1)
			}
		}
		processed, diagnostics := preprocess(src.text, o.CCFlags)
		processed, src.miningDiagnostics = preprocessMining(processed)
		if decoded.utf8BOM {
			processed = "   " + processed[3:]
		}
		src.preprocessorDiagnostics = diagnostics
		splitText, wrapped := wrappedUnits(processed)
		src.processed = splitText
		src.units = append(src.units, wrapped...)
		maxTokens := o.MaxTokens
		if maxTokens == 0 {
			maxTokens = 1000000
		}
		wrappedIndex := 0
		for _, seg := range parser.Split(splitText) {
			if seg.Empty() || seg.Kind != parser.SegmentSQL {
				continue
			}
			start := seg.ByteStart
			for wrappedIndex < len(wrapped) && wrapped[wrappedIndex].end <= seg.ByteEnd {
				w := wrapped[wrappedIndex]
				if w.start >= start {
					start = w.end
				}
				wrappedIndex++
			}
			u := unit{start: start, end: seg.ByteEnd}
			if tokenBudgetExceeded(splitText, start, seg.ByteEnd, maxTokens) {
				u.header = parseHeader(splitText, start, seg.ByteEnd)
				u.limitExceeded = true
				u.fallback = strings.Clone(splitText[start:seg.ByteEnd])
				src.units = append(src.units, u)
				continue
			}
			src.units = append(src.units, u)
		}
		sortUnits(src.units)
		s.sources = append(s.sources, src)
		return nil
	})
	if err != nil {
		return err
	}
	sort.Slice(s.sources, func(i, j int) bool { return s.sources[i].path < s.sources[j].path })
	if o.Metrics != nil {
		o.Metrics.Files = len(s.sources)
		for _, src := range s.sources {
			o.Metrics.Lines += len(src.lineStarts)
			o.Metrics.Units += len(src.units)
			for _, u := range src.units {
				if u.node != nil {
					o.Metrics.ParsedUnits++
				}
				if u.wrapped || u.limitExceeded || u.err != nil {
					o.Metrics.DegradedUnits++
				}
			}
		}
		parseStarted := time.Now()
		s.parseUnits(o.Workers)
		o.Metrics.ParseDuration = time.Since(parseStarted)
		// Parsing can turn units into errors or AST nodes; refresh these counters.
		o.Metrics.ParsedUnits, o.Metrics.DegradedUnits = 0, 0
		for _, src := range s.sources {
			for _, u := range src.units {
				if u.node != nil {
					o.Metrics.ParsedUnits++
				}
				if u.wrapped || u.limitExceeded || u.err != nil {
					o.Metrics.DegradedUnits++
				}
			}
		}
	} else {
		s.parseUnits(o.Workers)
	}
	for _, src := range s.sources {
		assignSchemas(src, pathSchema(src.path, o.Schema, pattern))
	}
	s.collectMethods()
	for _, src := range s.sources {
		s.declarations(src)
	}
	if o.Catalog != "" {
		catalogPath := o.Catalog
		if !filepath.IsAbs(catalogPath) {
			catalogPath = filepath.Join(root, catalogPath)
		}
		snapshot, err := readSnapshot(catalogPath)
		if err != nil {
			return err
		}
		s.mergeSnapshot(snapshot)
	}
	s.replayDDL()
	s.collectTypeParents()
	s.collectPackageRowTypes()
	// The full preprocessed source duplicates src.text and is no longer needed
	// after declarations and migration replay. Degraded units retain only their
	// own isolated text range for lexical reference recovery.
	for _, src := range s.sources {
		for i := range src.units {
			u := &src.units[i]
			if u.fallback == "" && u.err != nil && u.start >= 0 && u.end >= u.start && u.end <= len(src.processed) {
				u.fallback = strings.Clone(src.processed[u.start:u.end])
			}
		}
		src.processed = ""
	}
	if cacheDir != "" {
		stats := o.CacheStats
		if stats == nil {
			stats = &CacheStats{}
		}
		external := make(map[string]*scip.SymbolInformation, len(s.external))
		for symbol, info := range s.external {
			external[symbol] = info
		}
		cache := &documentCache{dir: cacheDir, stats: stats, external: external}
		cache.catalog = s.buildCacheCatalog()
		executable, err := executableFingerprint()
		if err != nil {
			cache.warn(err)
		} else {
			cache.executable = executable
			s.cache = cache
		}
	}
	// Node-to-method mapping is no longer needed after the declaration pass.
	s.methodNodes = nil
	out := o.Output
	if !filepath.IsAbs(out) {
		out = filepath.Join(root, out)
	}
	f, err := os.CreateTemp(filepath.Dir(out), ".scip-*")
	if err != nil {
		return err
	}
	temp := f.Name()
	defer os.Remove(temp)
	err = s.write(f)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(temp, out)
}

// parseRange isolates parser failures to one compilation unit.
func parseRange(text string, start, end int) (list *ast.List, err error) {
	defer func() {
		if value := recover(); value != nil {
			err = fmt.Errorf("parser panic: %v", value)
			list = nil
		}
	}()
	return parser.ParseRange(text, start, end)
}

func (s *state) prefix() string {
	return "scip-plsql oracle " + s.opts.Database + " " + s.opts.Version + " "
}
func name(n string) string {
	if len(n) > 1 && n[0] == '"' && n[len(n)-1] == '"' {
		return strings.ReplaceAll(n[1:len(n)-1], "\"\"", "\"")
	}
	return strings.ToUpper(n)
}
func escape(n string) string {
	for _, r := range n {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '$' || r == '+' || r == '-') {
			return "`" + strings.ReplaceAll(n, "`", "``") + "`"
		}
	}
	if n == "" {
		return "``"
	}
	return n
}
func (s *state) global(schema, obj, suffix string) string {
	return s.prefix() + escape(name(schema)) + "/" + escape(name(obj)) + suffix
}

// remoteGlobal places objects reached through a database link in a stable
// SCIP package unique to that link. Hex encoding keeps arbitrary quoted link
// names within the single package component of SCIP symbols.
func (s *state) remoteGlobal(dblink, schema, obj, suffix string) string {
	link := strings.ToUpper(hex.EncodeToString([]byte(name(dblink))))
	prefix := "scip-plsql oracle DBLINK_" + link + " " + s.opts.Version + " "
	return prefix + escape(name(schema)) + "/" + escape(name(obj)) + suffix
}

func (s *state) externalSymbol(symbol, display string, kind scip.SymbolInformation_Kind, parent string) {
	if symbol == "" {
		return
	}
	info := &scip.SymbolInformation{Symbol: symbol, DisplayName: display, Kind: kind, EnclosingSymbol: parent}
	if s.external[symbol] == nil {
		s.external[symbol] = info
	}
	if s.externalUsed != nil && s.externalUsed[symbol] == nil {
		s.externalUsed[symbol] = info
	}
}

func (s *state) remoteObject(dblink, schema, obj string) string {
	symbol := s.remoteGlobal(dblink, schema, obj, "#")
	s.externalSymbol(symbol, obj, scip.SymbolInformation_Struct, "")
	return symbol
}
func (s *state) declarations(src *source) {
	src.doc = &scip.Document{RelativePath: src.path, Language: "PLSQL", PositionEncoding: scip.PositionEncoding_UTF8CodeUnitOffsetFromLineStart}
	src.symbolMetadata = make(map[string]symbolMetadata)
	if src.embeddedText {
		src.doc.Text = src.text
	}
	for _, d := range src.preprocessorDiagnostics {
		src.doc.Occurrences = append(src.doc.Occurrences, &scip.Occurrence{Range: src.rng(d.start, d.end), Diagnostics: []*scip.Diagnostic{{Severity: scip.Severity_Warning, Code: "conditional-compilation", Source: "scip-plsql", Message: d.message}}})
	}
	for _, d := range src.miningDiagnostics {
		src.addDiagnostic(d.start, d.end, &scip.Diagnostic{Severity: scip.Severity_Warning, Code: "mining-model-unresolved", Source: "scip-plsql", Message: d.message})
	}
	for _, d := range src.encodingDiagnostics {
		src.doc.Occurrences = append(src.doc.Occurrences, &scip.Occurrence{Range: src.rng(d.start, d.end), Diagnostics: []*scip.Diagnostic{{Severity: scip.Severity_Warning, Code: "encoding-replacement", Source: "scip-plsql", Message: d.message}}})
	}
	for _, d := range src.schemaDiagnostics {
		src.doc.Occurrences = append(src.doc.Occurrences, &scip.Occurrence{Range: src.rng(d.start, d.end), Diagnostics: []*scip.Diagnostic{{Severity: scip.Severity_Warning, Code: "current-schema", Source: "scip-plsql", Message: d.message}}})
	}
	for _, u := range src.units {
		if u.wrapped || u.limitExceeded || u.err != nil {
			s.declareLexicalHeader(src, u.header, u.schema)
		} else {
			s.declareUnit(src, u)
		}
		if u.wrapped || u.limitExceeded {
			code, msg := "wrapped", "Código wrapped: solo se indexa la cabecera"
			if u.limitExceeded {
				code = "token-budget"
				msg = "Unidad degradada: se superó el presupuesto de tokens"
			}
			a, b := u.start, u.start+1
			if u.header != nil {
				a, b = u.header.start, u.header.end
			}
			src.addDiagnostic(a, b, &scip.Diagnostic{Severity: scip.Severity_Warning, Code: code, Source: "scip-plsql", Message: msg})
		}
		if u.err != nil {
			a := u.start
			for a < u.end && (src.text[a] == ' ' || src.text[a] == '\n' || src.text[a] == '\r' || src.text[a] == '\t') {
				a++
			}
			b := a + 1
			if b > u.end {
				b = u.end
			}
			if u.header != nil {
				a, b = u.header.start, u.header.end
			}
			src.addDiagnostic(a, b, &scip.Diagnostic{Severity: scip.Severity_Warning, Code: "parse-error", Source: "scip-plsql", Message: u.err.Error()})
		}
	}
}

// Attach a diagnostic to an existing occurrence when both describe the same
// range; otherwise SCIP permits a diagnostic-only occurrence with no symbol.
func (src *source) addDiagnostic(a, b int, diagnostic *scip.Diagnostic) {
	r := src.rng(a, b)
	for i := len(src.doc.Occurrences) - 1; i >= 0; i-- {
		o := src.doc.Occurrences[i]
		if o.Symbol == "" || len(o.Range) != len(r) {
			continue
		}
		same := true
		for j := range r {
			if o.Range[j] != r[j] {
				same = false
				break
			}
		}
		if same {
			o.Diagnostics = append(o.Diagnostics, diagnostic)
			return
		}
	}
	src.doc.Occurrences = append(src.doc.Occurrences, &scip.Occurrence{Range: r, Diagnostics: []*scip.Diagnostic{diagnostic}})
}

func sortSymbols(src *source) {
	sort.Slice(src.doc.Symbols, func(i, j int) bool { return src.doc.Symbols[i].Symbol < src.doc.Symbols[j].Symbol })
	if len(src.doc.Symbols) > 0 {
		symbols := src.doc.Symbols[:1]
		for _, v := range src.doc.Symbols[1:] {
			if v.Symbol != symbols[len(symbols)-1].Symbol {
				symbols = append(symbols, v)
			}
		}
		src.doc.Symbols = symbols
	}
}
func tokenAt(text string, start int) (string, int, int) {
	if start < 0 || start >= len(text) {
		return "", 0, 0
	}
	i := start
	for i < len(text) && (text[i] == ' ' || text[i] == '\n' || text[i] == '\r' || text[i] == '\t') {
		i++
	}
	j := i
	if j < len(text) && text[j] == '"' {
		j++
		for j < len(text) {
			if text[j] == '"' {
				j++
				if j < len(text) && text[j] == '"' {
					j++
					continue
				}
				break
			}
			j++
		}
	} else {
		for j < len(text) {
			c := text[j]
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '$' || c == '#' || c >= 128) {
				break
			}
			j++
		}
	}
	return text[i:j], i, j
}
func objectToken(text string, n *ast.ObjectName) (string, int, int) {
	if n == nil {
		return "", 0, 0
	}
	t, a, b := tokenAt(text, n.Loc.Start)
	if n.Schema != "" {
		dot := b
		for dot < len(text) && strings.ContainsRune(" \t\r\n", rune(text[dot])) {
			dot++
		}
		if dot >= len(text) || text[dot] != '.' {
			return "", 0, 0
		}
		t, a, b = tokenAt(text, dot+1)
	}
	return t, a, b
}
func (src *source) position(off int) (int32, int32) {
	if off < 0 {
		off = 0
	}
	if off > len(src.text) {
		off = len(src.text)
	}
	if len(src.offsetMap) != 0 {
		if off >= len(src.offsetMap) {
			off = len(src.offsetMap) - 1
		}
		off = src.offsetMap[off]
	}
	line := sort.Search(len(src.lineStarts), func(i int) bool { return src.lineStarts[i] > off }) - 1
	if line < 0 {
		return 0, int32(off)
	}
	return int32(line), int32(off - src.lineStarts[line])
}
func (src *source) rng(a, b int) []int32 {
	l, c := src.position(a)
	el, ec := src.position(b)
	if l == el {
		return []int32{l, c, ec}
	}
	return []int32{l, c, el, ec}
}
func add(src *source, d decl) {
	if d.end <= d.start {
		return
	}
	occurrence := &scip.Occurrence{Range: src.rng(d.start, d.end), Symbol: d.symbol, SymbolRoles: d.role}
	info := &scip.SymbolInformation{Symbol: d.symbol, DisplayName: d.name, Kind: d.kind}
	if metadata, ok := src.symbolMetadata[d.symbol]; ok {
		if metadata.enclosingEnd > metadata.enclosingStart {
			occurrence.EnclosingRange = src.rng(metadata.enclosingStart, metadata.enclosingEnd)
		}
		info.EnclosingSymbol = metadata.enclosingSymbol
		if metadata.signature != "" {
			info.SignatureDocumentation = &scip.Document{Text: metadata.signature}
		}
		if metadata.documentation != "" {
			info.Documentation = []string{metadata.documentation}
		}
	}
	src.doc.Occurrences = append(src.doc.Occurrences, occurrence)
	src.doc.Symbols = append(src.doc.Symbols, info)
}
func (s *state) declareUnit(src *source, u unit) {
	var obj *ast.ObjectName
	var suffix string
	var kind scip.SymbolInformation_Kind
	var body bool
	var members *ast.List
	var typeMethods *ast.List
	switch n := u.node.(type) {
	case *ast.CreatePackageStmt:
		obj = n.Name
		suffix = "#"
		kind = scip.SymbolInformation_Package
		body = n.IsBody
		members = n.Body
	case *ast.CreateTypeStmt:
		obj = n.Name
		suffix = "#"
		kind = scip.SymbolInformation_Class
		body = n.IsBody
		members = n.Attributes
		if n.IsBody {
			typeMethods = n.Body
		} else {
			typeMethods = n.Methods
		}
	case *ast.CreateTableStmt:
		obj = n.Name
		suffix = "#"
		kind = scip.SymbolInformation_Struct
		members = n.Columns
	case *ast.CreateViewStmt:
		obj = n.Name
		suffix = "#"
		kind = scip.SymbolInformation_Struct
	case *ast.CreateProcedureStmt:
		obj = n.Name
		suffix = "()."
		kind = scip.SymbolInformation_Function
		members = n.Parameters
	case *ast.CreateFunctionStmt:
		obj = n.Name
		suffix = "()."
		kind = scip.SymbolInformation_Function
		members = n.Parameters
	case *ast.CreateTriggerStmt:
		obj = n.Name
		suffix = "."
		kind = scip.SymbolInformation_Function
	case *ast.CreateSequenceStmt:
		obj = n.Name
		suffix = "."
		kind = scip.SymbolInformation_Variable
	case *ast.CreateSynonymStmt:
		obj = n.Name
		suffix = "#"
		kind = scip.SymbolInformation_TypeAlias
	default:
		return
	}
	if obj == nil {
		return
	}
	schema := u.schema
	if obj.Schema != "" {
		schema, _, _ = tokenAt(src.text, obj.Loc.Start)
	}
	if n, ok := u.node.(*ast.CreateSynonymStmt); ok && n.Public {
		schema = "PUBLIC"
	}
	if schema == "" {
		return
	}
	raw, a, b := objectToken(src.text, obj)
	sym := s.global(schema, raw, suffix)
	headerStart, _, signature := declarationSignature(src.text, u.start, u.end)
	setSymbolMetadata(src, sym, symbolMetadata{
		enclosingStart: u.start, enclosingEnd: u.end,
		documentation: leadingDocumentation(src.text, headerStart), signature: signature,
	})
	role := int32(scip.SymbolRole_Definition)
	if !body {
		switch u.node.(type) {
		case *ast.CreatePackageStmt, *ast.CreateTypeStmt:
			role = int32(scip.SymbolRole_ForwardDefinition)
		}
	}
	add(src, decl{sym, name(raw), a, b, role, kind})
	key := name(schema) + "." + name(raw)
	s.objects[key] = sym
	s.objectKinds[key] = kind
	delete(s.synonyms, key)
	if synonymNode, ok := u.node.(*ast.CreateSynonymStmt); ok && synonymNode.Target != nil {
		// An unqualified synonym target uses the creator's schema, including
		// for PUBLIC synonyms; it is not owned by PUBLIC.
		targetSchema := u.schema
		s.synonyms[key] = synonym{target: targetKey(src, synonymNode.Target, targetSchema), remote: synonymNode.Target.DBLink != ""}
	}
	var allMembers []ast.Node
	if members != nil {
		allMembers = append(allMembers, members.Items...)
	}
	if typeMethods != nil {
		for _, item := range typeMethods.Items {
			if method, ok := item.(*ast.TypeBodyMember); ok && method.Subprog != nil {
				allMembers = append(allMembers, method.Subprog)
			}
		}
	}
	if len(allMembers) == 0 {
		return
	}
	counts := map[string]int{}
	for _, m := range allMembers {
		var loc ast.Loc
		var nm, tail string
		var k scip.SymbolInformation_Kind
		switch x := m.(type) {
		case *ast.ColumnDef:
			loc = x.Loc
			nm = x.Name
			tail = "."
			k = scip.SymbolInformation_Field
		case *ast.Parameter:
			loc = x.Loc
			nm = x.Name
			tail = "parameter"
			k = scip.SymbolInformation_Parameter
		case *ast.PLSQLVarDecl:
			loc = x.Loc
			nm = x.Name
			tail = "."
			k = scip.SymbolInformation_Variable
		case *ast.PLSQLCursorDecl:
			loc = x.Loc
			nm = x.Name
			tail = "."
			k = scip.SymbolInformation_Variable
		case *ast.CreateProcedureStmt:
			if x.Name == nil {
				continue
			}
			loc = x.Name.Loc
			nm = x.Name.Name
			tail = "method"
			k = scip.SymbolInformation_Method
		case *ast.CreateFunctionStmt:
			if x.Name == nil {
				continue
			}
			loc = x.Name.Loc
			nm = x.Name.Name
			tail = "method"
			k = scip.SymbolInformation_Method
		default:
			continue
		}
		raw, a, b := tokenAt(src.text, loc.Start)
		if name(raw) != nm {
			continue
		}
		member := escape(name(raw))
		if tail == "method" {
			n := counts[name(raw)]
			counts[name(raw)]++
			if n == 0 {
				tail = "()."
			} else {
				tail = fmt.Sprintf("(+%d).", n)
			}
		} else if tail == "parameter" {
			member = "(" + member + ")"
			tail = ""
		}
		ms := sym + member + tail
		if method := s.methodNodes[m]; method != nil {
			ms = method.symbol
		}
		memberStart, memberEnd := u.start, u.end
		if start, end, ok := astNodeRange(m); ok && start >= u.start && end <= u.end && end > start {
			memberStart, memberEnd = start, end
		}
		signatureStart, _, memberSignature := declarationSignature(src.text, memberStart, memberEnd)
		setSymbolMetadata(src, ms, symbolMetadata{
			enclosingSymbol: sym, enclosingStart: memberStart, enclosingEnd: memberEnd,
			documentation: leadingDocumentation(src.text, signatureStart), signature: memberSignature,
		})
		add(src, decl{ms, name(raw), a, b, role, k})
		s.objects[key+"."+name(raw)] = ms
		if _, isType := u.node.(*ast.CreateTypeStmt); isType {
			if _, isField := m.(*ast.ColumnDef); isField {
				if s.typeBindings[key] == nil {
					s.typeBindings[key] = map[string]string{}
				}
				s.typeBindings[key][name(raw)] = ms
			}
		}
		if _, isPackage := u.node.(*ast.CreatePackageStmt); isPackage {
			switch m.(type) {
			case *ast.PLSQLVarDecl, *ast.PLSQLCursorDecl:
				if s.packageBindings[key] == nil {
					s.packageBindings[key] = map[string]string{}
				}
				s.packageBindings[key][name(raw)] = ms
			}
		}
	}
}
func (s *state) references(src *source) {
	existing := map[string]bool{}
	for _, o := range src.doc.Occurrences {
		existing[fmt.Sprint(o.Range)] = true
	}
	addRef := func(a, b int, symbol string, role scip.SymbolRole) {
		if symbol == "" || a < 0 || b <= a {
			return
		}
		r := src.rng(a, b)
		key := fmt.Sprint(r)
		if existing[key] {
			return
		}
		existing[key] = true
		src.doc.Occurrences = append(src.doc.Occurrences, &scip.Occurrence{Range: r, Symbol: symbol, SymbolRoles: int32(role)})
	}
	addAmbiguous := func(a, b int, symbols []string) {
		r := src.rng(a, b)
		k := fmt.Sprint(r)
		if existing[k] {
			return
		}
		existing[k] = true
		for _, symbol := range symbols {
			src.doc.Occurrences = append(src.doc.Occurrences, &scip.Occurrence{Range: r, Symbol: symbol, SymbolRoles: int32(scip.SymbolRole_ReadAccess), Diagnostics: []*scip.Diagnostic{{Severity: scip.Severity_Warning, Code: "ambiguous-overload", Source: "scip-plsql", Message: "La llamada coincide con varias sobrecargas"}}})
		}
	}
	for _, u := range src.units {
		if u.node == nil {
			if u.err != nil || u.limitExceeded {
				s.resolveLexicalReferences(src, u, addRef)
			}
			continue
		}
		currentPackage := ""
		var container *ast.ObjectName
		switch n := u.node.(type) {
		case *ast.CreatePackageStmt:
			container = n.Name
		case *ast.CreateTypeStmt:
			container = n.Name
		}
		if container != nil {
			sch := effectiveSchema(src, u)
			raw, _, _ := objectToken(src.text, container)
			currentPackage = name(sch) + "." + name(raw)
		}
		handledCalls := map[*ast.ObjectName]bool{}
		handledSelect := map[*ast.SelectStmt]bool{}
		handledObjects := map[*ast.ObjectName]bool{}
		schema := effectiveSchema(src, u)
		dml := dmlResolver{state: s, src: src, schema: schema, emit: addRef, handledSelect: handledSelect, handledObjects: handledObjects}
		ast.Inspect(u.node, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.PLSQLCall:
				if receiver, ok := src.typedProcedureCalls[x]; ok {
					if col, valid := x.Name.(*ast.ColumnRef); valid {
						fake := &ast.FuncCallExpr{FuncName: &ast.ObjectName{Loc: col.Loc}}
						s.resolveInstanceCall(src, fake, receiver, addRef, addAmbiguous)
					}
					return true
				}
				if col, ok := x.Name.(*ast.ColumnRef); ok && !src.shadowedProcedureCalls[x] {
					obj := &ast.ObjectName{Loc: col.Loc}
					fake := &ast.FuncCallExpr{FuncName: obj}
					if len(nameParts(src.text, obj)) > 1 || currentPackage != "" {
						s.resolveCall(src, fake, schema, currentPackage, addRef, addAmbiguous)
					}
				}
			case *ast.FuncCallExpr:
				if call, ok := src.typedCalls[x]; ok {
					handledCalls[x.FuncName] = true
					s.resolveInstanceCall(src, x, call, addRef, addAmbiguous)
					return true
				}
				if x.FuncName != nil {
					if src.shadowedCalls[x] {
						handledCalls[x.FuncName] = true
						return true
					}
					handledCalls[x.FuncName] = true
					s.resolveCall(src, x, schema, currentPackage, addRef, addAmbiguous)
				}
			case *ast.SelectStmt:
				if handledSelect[x] {
					return false
				}
				s.resolveSelect(src, x, schema, nil, addRef)
				s.resolveCallsInAST(src, x, schema, currentPackage, addRef, addAmbiguous)
				return false
			case *ast.ObjectName:
				if handledCalls[x] || handledObjects[x] {
					return true
				}
				sch := schema
				if x.Schema != "" {
					sch, _, _ = tokenAt(src.text, x.Loc.Start)
				}
				raw, a, b := objectToken(src.text, x)
				if x.DBLink != "" {
					addRef(a, b, s.remoteObject(x.DBLink, sch, raw), scip.SymbolRole_ReadAccess)
					return true
				}
				_, symbol := s.resolveKey(sch, raw, x.Schema == "")
				addRef(a, b, symbol, scip.SymbolRole_ReadAccess)
			case *ast.InsertStmt:
				dml.insert(x)
				s.resolveCallsInAST(src, x, schema, currentPackage, addRef, addAmbiguous)
				if x.Table != nil && x.Table.DBLink != "" {
					sch := schema
					if x.Table.Schema != "" {
						sch, _, _ = tokenAt(src.text, x.Table.Loc.Start)
					}
					raw, a, b := objectToken(src.text, x.Table)
					addRef(a, b, s.remoteObject(x.Table.DBLink, sch, raw), scip.SymbolRole_WriteAccess)
				} else if x.Table != nil && x.Table.DBLink == "" && x.Dblink == "" && x.Columns != nil {
					sch := schema
					if x.Table.Schema != "" {
						sch = x.Table.Schema
					}
					table, _ := s.resolveKey(sch, x.Table.Name, x.Table.Schema == "")
					for _, item := range x.Columns.Items {
						if col, ok := item.(*ast.ColumnRef); ok {
							raw, a, b := columnToken(src.text, col)
							addRef(a, b, s.objects[table+"."+name(raw)], scip.SymbolRole_WriteAccess)
						}
					}
				}
			case *ast.UpdateStmt:
				dml.update(x)
				s.resolveCallsInAST(src, x, schema, currentPackage, addRef, addAmbiguous)
			case *ast.DeleteStmt:
				dml.delete(x)
				s.resolveCallsInAST(src, x, schema, currentPackage, addRef, addAmbiguous)
			case *ast.MergeStmt:
				dml.merge(x)
				s.resolveCallsInAST(src, x, schema, currentPackage, addRef, addAmbiguous)
			case *ast.PLSQLExecImmediate:
				s.resolveDynamicSQL(src, x.SQL, schema, currentPackage, x.Loc.Start, x.Loc.End, addRef, addAmbiguous)
			case *ast.DynamicSQLExpr:
				s.resolveDynamicSQL(src, x.SQL, schema, currentPackage, x.Loc.Start, x.Loc.End, addRef, addAmbiguous)
			}
			return true
		})
	}
	sort.SliceStable(src.doc.Occurrences, func(i, j int) bool {
		a, b := src.doc.Occurrences[i].Range, src.doc.Occurrences[j].Range
		if a[0] != b[0] {
			return a[0] < b[0]
		}
		return a[1] < b[1]
	})
}
func writeBytesField(w io.Writer, field protowire.Number, b []byte) error {
	prefix := protowire.AppendTag(nil, field, protowire.BytesType)
	prefix = protowire.AppendVarint(prefix, uint64(len(b)))
	if _, err := w.Write(prefix); err != nil {
		return err
	}
	_, err := w.Write(b)
	return err
}
func writeField(w io.Writer, field protowire.Number, m proto.Message) error {
	b, err := proto.MarshalOptions{Deterministic: true}.Marshal(m)
	if err != nil {
		return err
	}
	return writeBytesField(w, field, b)
}
func (s *state) write(w io.Writer) error {
	uri := (&url.URL{Scheme: "file", Path: filepath.ToSlash(s.opts.Root)}).String()
	encoding := scip.TextEncoding_UTF8
	if s.seenOther || s.seenUTF16 && s.seenUTF8 {
		encoding = scip.TextEncoding_UnspecifiedTextEncoding
	} else if s.seenUTF16 {
		encoding = scip.TextEncoding_UTF16
	}
	meta := &scip.Metadata{Version: scip.ProtocolVersion_UnspecifiedProtocolVersion, ToolInfo: &scip.ToolInfo{Name: "scip-plsql", Version: "0.1.0"}, ProjectRoot: uri, TextDocumentEncoding: encoding}
	if err := writeField(w, 1, meta); err != nil {
		return err
	}
	// The catalog is immutable here; serialize and release each resolved
	// document in path order, keeping the output deterministic.
	for _, src := range s.sources {
		var key [32]byte
		var cached []byte
		var cachedDoc *scip.Document
		if s.cache != nil {
			var err error
			key, err = s.cache.key(src, s)
			if err != nil {
				return err
			}
			entry, data := s.cache.get(key, src.path)
			cached = data
			if entry != nil {
				cachedDoc = entry.Documents[0]
				for _, info := range entry.ExternalSymbols {
					if s.external[info.Symbol] == nil {
						s.external[info.Symbol] = info
					}
				}
			}
		}
		if cached == nil {
			if s.cache != nil {
				s.externalUsed = make(map[string]*scip.SymbolInformation)
			}
			s.attachSynonymRelationships(src)
			s.attachTypeRelationships(src)
			s.resolveLocals(src)
			s.references(src)
			sortSymbols(src)
			var err error
			cached, err = proto.MarshalOptions{Deterministic: true}.Marshal(src.doc)
			if err != nil {
				return err
			}
			if s.cache != nil {
				keys := make([]string, 0, len(s.externalUsed))
				for symbol := range s.externalUsed {
					keys = append(keys, symbol)
				}
				sort.Strings(keys)
				entry := &scip.Index{Documents: []*scip.Document{src.doc}}
				for _, symbol := range keys {
					entry.ExternalSymbols = append(entry.ExternalSymbols, s.externalUsed[symbol])
				}
				payload, err := proto.MarshalOptions{Deterministic: true}.Marshal(entry)
				if err != nil {
					return err
				}
				s.cache.put(key, payload)
				s.externalUsed = nil
			}
			cachedDoc = src.doc
		}
		if s.opts.Metrics != nil && cachedDoc != nil {
			mask := int32(scip.SymbolRole_ReadAccess | scip.SymbolRole_WriteAccess)
			for _, occurrence := range cachedDoc.Occurrences {
				if occurrence.Symbol != "" && occurrence.SymbolRoles&mask != 0 {
					s.opts.Metrics.ResolvedReferences++
				}
			}
		}
		if err := writeBytesField(w, 2, cached); err != nil {
			return err
		}
		src.doc = nil
		src.units = nil
		src.text = ""
		src.processed = ""
		src.lineStarts = nil
		src.sqlBindings = nil
		src.shadowedCalls = nil
		src.shadowedProcedureCalls = nil
		src.typedCalls = nil
		src.typedProcedureCalls = nil
	}
	return s.writeExternal(w)
}
