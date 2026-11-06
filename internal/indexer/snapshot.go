package indexer

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/sourcegraph/scip/bindings/go/scip"
)

// CatalogSnapshot is an offline dictionary snapshot. Names are Oracle
// dictionary spellings: ordinary identifiers are uppercase and mixed-case
// identifiers retain their exact case.
type CatalogSnapshot struct {
	Objects []CatalogObject `json:"objects"`
}
type CatalogObject struct {
	Schema        string             `json:"schema"`
	Name          string             `json:"name"`
	Kind          string             `json:"kind"`
	Documentation string             `json:"documentation,omitempty"`
	ReturnType    string             `json:"return_type,omitempty"`
	Columns       []CatalogColumn    `json:"columns,omitempty"`
	Members       []CatalogMember    `json:"members,omitempty"`
	Parameters    []CatalogParameter `json:"parameters,omitempty"`
	Target        *CatalogTarget     `json:"target,omitempty"`
}
type CatalogColumn struct {
	Name string `json:"name"`
	Type string `json:"type,omitempty"`
}
type CatalogTarget struct {
	Schema string `json:"schema"`
	Name   string `json:"name"`
	DBLink string `json:"dblink,omitempty"`
}
type CatalogMember struct {
	Name       string             `json:"name"`
	Kind       string             `json:"kind"`
	ReturnType string             `json:"return_type,omitempty"`
	Parameters []CatalogParameter `json:"parameters,omitempty"`
}
type CatalogParameter struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Mode    string `json:"mode,omitempty"`
	Default bool   `json:"default,omitempty"`
}

// dictionarySpelling protects case and embedded double quotes when passing a
// dictionary identifier through the same normalization as source identifiers.
func dictionarySpelling(n string) string {
	if n == strings.ToUpper(n) {
		return n
	}
	return `"` + strings.ReplaceAll(n, `"`, `""`) + `"`
}
func catalogKind(kind string) (string, scip.SymbolInformation_Kind, bool) {
	switch strings.ToLower(kind) {
	case "table", "view":
		return "#", scip.SymbolInformation_Struct, true
	case "package":
		return "#", scip.SymbolInformation_Package, true
	case "type":
		return "#", scip.SymbolInformation_Class, true
	case "procedure", "function":
		return "().", scip.SymbolInformation_Function, true
	case "sequence":
		return ".", scip.SymbolInformation_Variable, true
	case "synonym":
		return "#", scip.SymbolInformation_TypeAlias, true
	}
	return "", 0, false
}
func validCatalogName(n string) bool { return n != "" && !strings.ContainsAny(n, "\x00\r\n") }
func readSnapshot(path string) (CatalogSnapshot, error) {
	var snap CatalogSnapshot
	f, err := os.Open(path)
	if err != nil {
		return snap, fmt.Errorf("abrir catálogo: %w", err)
	}
	defer f.Close()
	decoder := json.NewDecoder(f)
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&snap); err != nil {
		return snap, fmt.Errorf("leer catálogo: %w", err)
	}
	var extra any
	if err = decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			err = fmt.Errorf("contenido adicional")
		}
		return snap, fmt.Errorf("leer catálogo: %w", err)
	}
	seen := map[string]bool{}
	for _, obj := range snap.Objects {
		if !validCatalogName(obj.Schema) || !validCatalogName(obj.Name) {
			return snap, fmt.Errorf("catálogo: objeto sin esquema o nombre válido")
		}
		key := obj.Schema + "\x00" + obj.Name
		if seen[key] {
			return snap, fmt.Errorf("catálogo: objeto duplicado %s.%s", obj.Schema, obj.Name)
		}
		seen[key] = true
		_, _, ok := catalogKind(obj.Kind)
		if !ok {
			return snap, fmt.Errorf("catálogo: tipo %q desconocido para %s.%s", obj.Kind, obj.Schema, obj.Name)
		}
		kind := strings.ToLower(obj.Kind)
		if kind == "synonym" {
			if obj.Target == nil || !validCatalogName(obj.Target.Schema) || !validCatalogName(obj.Target.Name) {
				return snap, fmt.Errorf("catálogo: destino inválido para %s.%s", obj.Schema, obj.Name)
			}
		} else if obj.Target != nil {
			return snap, fmt.Errorf("catálogo: destino en objeto que no es sinónimo %s.%s", obj.Schema, obj.Name)
		}
		if len(obj.Columns) > 0 && kind != "table" && kind != "view" && kind != "type" {
			return snap, fmt.Errorf("catálogo: columnas no válidas en %s.%s", obj.Schema, obj.Name)
		}
		colNames := map[string]bool{}
		for _, col := range obj.Columns {
			if !validCatalogName(col.Name) || colNames[col.Name] {
				return snap, fmt.Errorf("catálogo: columna inválida o duplicada en %s.%s", obj.Schema, obj.Name)
			}
			colNames[col.Name] = true
		}
		if len(obj.Parameters) > 0 && kind != "procedure" && kind != "function" {
			return snap, fmt.Errorf("catálogo: parámetros no válidos en %s.%s", obj.Schema, obj.Name)
		}
		if obj.ReturnType != "" && kind != "function" {
			return snap, fmt.Errorf("catálogo: tipo de retorno no válido en %s.%s", obj.Schema, obj.Name)
		}
		if err := validateCatalogParameters(obj.Parameters); err != nil {
			return snap, fmt.Errorf("catálogo: parámetros de %s.%s: %w", obj.Schema, obj.Name, err)
		}
		if len(obj.Members) > 0 && kind != "package" && kind != "type" {
			return snap, fmt.Errorf("catálogo: miembros no válidos en %s.%s", obj.Schema, obj.Name)
		}
		for _, m := range obj.Members {
			if !validCatalogName(m.Name) || (strings.ToLower(m.Kind) != "procedure" && strings.ToLower(m.Kind) != "function") {
				return snap, fmt.Errorf("catálogo: miembro inválido en %s.%s", obj.Schema, obj.Name)
			}
			if m.ReturnType != "" && strings.ToLower(m.Kind) != "function" {
				return snap, fmt.Errorf("catálogo: tipo de retorno no válido en %s.%s.%s", obj.Schema, obj.Name, m.Name)
			}
			if err := validateCatalogParameters(m.Parameters); err != nil {
				return snap, fmt.Errorf("catálogo: parámetros de %s.%s.%s: %w", obj.Schema, obj.Name, m.Name, err)
			}
		}
	}
	return snap, nil
}

func validateCatalogParameters(params []CatalogParameter) error {
	seen := map[string]bool{}
	for _, p := range params {
		if !validCatalogName(p.Name) || p.Type == "" || seen[p.Name] {
			return fmt.Errorf("nombre o tipo inválido")
		}
		seen[p.Name] = true
		mode := strings.Join(strings.Fields(strings.ToUpper(p.Mode)), " ")
		switch mode {
		case "", "IN", "OUT", "IN OUT", "OUT NOCOPY", "IN OUT NOCOPY":
		default:
			return fmt.Errorf("modo desconocido %q", p.Mode)
		}
	}
	return nil
}

// mergeSnapshot gives repository declarations precedence while augmenting
// table/view columns that are not present in the repository.
func (s *state) mergeSnapshot(snap CatalogSnapshot) {
	for _, obj := range snap.Objects {
		schema, object := dictionarySpelling(obj.Schema), dictionarySpelling(obj.Name)
		key := obj.Schema + "." + obj.Name
		suffix, kind, _ := catalogKind(obj.Kind)
		symbol := s.global(schema, object, suffix)
		existing, repoExists := s.objects[key]
		if !repoExists {
			s.objects[key] = symbol
			s.objectKinds[key] = kind
			info := &scip.SymbolInformation{Symbol: symbol, DisplayName: obj.Name, Kind: kind}
			if obj.Documentation != "" {
				info.Documentation = []string{obj.Documentation}
			}
			if strings.EqualFold(obj.Kind, "procedure") || strings.EqualFold(obj.Kind, "function") {
				info.SignatureDocumentation = &scip.Document{Text: snapshotSubprogramSignature(obj.Kind, obj.Name, obj.Parameters, obj.ReturnType)}
			}
			s.external[symbol] = info
			if strings.EqualFold(obj.Kind, "synonym") {
				s.synonyms[key] = synonym{target: obj.Target.Schema + "." + obj.Target.Name, remote: obj.Target.DBLink != ""}
				if obj.Target.DBLink != "" {
					target := s.remoteObject(obj.Target.DBLink, obj.Target.Schema, obj.Target.Name)
					info.Relationships = []*scip.Relationship{{Symbol: target, IsReference: true}}
				}
			}
		} else if existing != symbol || s.objectKinds[key] != kind {
			continue
		}
		for _, col := range obj.Columns {
			memberKey := key + "." + col.Name
			if existing, exists := s.objects[memberKey]; exists {
				if kind == scip.SymbolInformation_Class {
					if s.typeBindings[key] == nil {
						s.typeBindings[key] = map[string]string{}
					}
					s.typeBindings[key][col.Name] = existing
				}
				continue
			}
			memberSym := symbol + escape(col.Name) + "."
			s.objects[memberKey] = memberSym
			if kind == scip.SymbolInformation_Class {
				if s.typeBindings[key] == nil {
					s.typeBindings[key] = map[string]string{}
				}
				s.typeBindings[key][col.Name] = memberSym
			}
			s.external[memberSym] = &scip.SymbolInformation{Symbol: memberSym, DisplayName: col.Name, Kind: scip.SymbolInformation_Field, EnclosingSymbol: symbol}
		}
		if repoExists {
			continue
		}
		if strings.EqualFold(obj.Kind, "procedure") || strings.EqualFold(obj.Kind, "function") {
			var params []formal
			for _, p := range obj.Parameters {
				params = append(params, formal{name: p.Name, typ: strings.ToUpper(p.Type), mode: parameterMode(p.Mode), optional: p.Default})
				paramSym := symbol + "(" + escape(p.Name) + ")"
				s.external[paramSym] = &scip.SymbolInformation{Symbol: paramSym, DisplayName: p.Name, Kind: scip.SymbolInformation_Parameter, EnclosingSymbol: symbol}
			}
			s.standalone[key] = &method{symbol: symbol, params: params, returnType: strings.ToUpper(obj.ReturnType), public: true}
		}
		counts := map[string]int{}
		for _, member := range obj.Members {
			n := counts[member.Name]
			counts[member.Name]++
			tail := "()."
			if n > 0 {
				tail = fmt.Sprintf("(+%d).", n)
			}
			methodSym := symbol + escape(member.Name) + tail
			var formals []formal
			for _, p := range member.Parameters {
				formals = append(formals, formal{name: p.Name, typ: strings.ToUpper(p.Type), mode: parameterMode(p.Mode), optional: p.Default})
				paramSym := methodSym + "(" + escape(p.Name) + ")"
				s.external[paramSym] = &scip.SymbolInformation{Symbol: paramSym, DisplayName: p.Name, Kind: scip.SymbolInformation_Parameter, EnclosingSymbol: methodSym}
			}
			s.methods[key+"."+member.Name] = append(s.methods[key+"."+member.Name], &method{symbol: methodSym, params: formals, returnType: strings.ToUpper(member.ReturnType), public: true})
			s.external[methodSym] = &scip.SymbolInformation{
				Symbol: methodSym, DisplayName: member.Name, Kind: scip.SymbolInformation_Method,
				EnclosingSymbol:        symbol,
				SignatureDocumentation: &scip.Document{Text: snapshotSubprogramSignature(member.Kind, member.Name, member.Parameters, member.ReturnType)},
			}
		}
	}
	// Resolve relationships only after all snapshot objects are merged.
	for key, syn := range s.synonyms {
		if syn.remote {
			continue
		}
		alias := s.objects[key]
		info := s.external[alias]
		if info == nil {
			continue
		}
		_, target := s.resolveCanonicalKey(syn.target)
		if target != "" {
			info.Relationships = []*scip.Relationship{{Symbol: target, IsReference: true}}
		}
	}
}

func snapshotSubprogramSignature(kind, name string, params []CatalogParameter, returnType string) string {
	kind = strings.ToUpper(strings.TrimSpace(kind))
	var signature strings.Builder
	signature.WriteString(kind)
	signature.WriteByte(' ')
	signature.WriteString(name)
	signature.WriteByte('(')
	for i, parameter := range params {
		if i > 0 {
			signature.WriteString(", ")
		}
		mode := strings.ToUpper(strings.TrimSpace(parameter.Mode))
		if mode == "" {
			mode = "IN"
		}
		signature.WriteString(parameter.Name)
		signature.WriteByte(' ')
		signature.WriteString(mode)
		signature.WriteByte(' ')
		signature.WriteString(strings.TrimSpace(parameter.Type))
		if parameter.Default {
			signature.WriteString(" DEFAULT")
		}
	}
	signature.WriteByte(')')
	if strings.EqualFold(kind, "FUNCTION") && strings.TrimSpace(returnType) != "" {
		signature.WriteString(" RETURN ")
		signature.WriteString(strings.TrimSpace(returnType))
	}
	return signature.String()
}
func (s *state) writeExternal(w io.Writer) error {
	keys := make([]string, 0, len(s.external))
	for k := range s.external {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if err := writeField(w, 3, s.external[key]); err != nil {
			return err
		}
	}
	return nil
}
