package indexer

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/scip-code/scip/bindings/go/scip"
	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/charmap"
	textunicode "golang.org/x/text/encoding/unicode"
	"golang.org/x/text/encoding/unicode/utf32"
	"google.golang.org/protobuf/proto"
)

func encodeFixture(t *testing.T, enc interface{ NewEncoder() *encoding.Encoder }, text string) []byte {
	t.Helper()
	out, err := enc.NewEncoder().String(text)
	if err != nil {
		t.Fatal(err)
	}
	return []byte(out)
}

func TestMixedSourceEncodingsAndBytePositions(t *testing.T) {
	root := t.TempDir()
	cpText := "/* € ñ */ CREATE TABLE hr.euro_cp (id NUMBER);\n"
	latinText := "/* € ñ */ CREATE TABLE hr.euro_latin (id NUMBER);\n"
	utf16Text := "/* ñ */ CREATE TABLE hr.wide_table (id NUMBER);\n"
	utf32Text := "/* 🚀 */ CREATE TABLE hr.wide32_table (id NUMBER);\n"
	bomText := "\ufeffCREATE TABLE hr.bom_table (id NUMBER);\n"
	files := map[string][]byte{"a_cp.sql": encodeFixture(t, charmap.Windows1252, cpText), "b_latin.sql": encodeFixture(t, charmap.ISO8859_15, latinText), "c_utf16.sql": encodeFixture(t, textunicode.UTF16(textunicode.LittleEndian, textunicode.UseBOM), utf16Text), "d_bom.sql": []byte(bomText), "e_utf32.sql": encodeFixture(t, utf32.UTF32(utf32.BigEndian, utf32.UseBOM), utf32Text)}
	for file, data := range files {
		if err := os.WriteFile(filepath.Join(root, file), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	out := filepath.Join(t.TempDir(), "index.scip")
	if err := Index(Options{Root: root, Output: out, Database: "demo", Version: "1", Schema: "HR"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	var idx scip.Index
	if err := proto.Unmarshal(data, &idx); err != nil {
		t.Fatal(err)
	}
	if idx.Metadata.TextDocumentEncoding != scip.TextEncoding_UnspecifiedTextEncoding {
		t.Fatalf("mixed metadata encoding: %v", idx.Metadata.TextDocumentEncoding)
	}
	tests := map[string]struct {
		text, token, symbol string
		embedded            bool
	}{"a_cp.sql": {cpText, "euro_cp", "HR/EURO_CP#", true}, "b_latin.sql": {latinText, "euro_latin", "HR/EURO_LATIN#", true}, "c_utf16.sql": {utf16Text, "wide_table", "HR/WIDE_TABLE#", true}, "d_bom.sql": {bomText, "bom_table", "HR/BOM_TABLE#", false}, "e_utf32.sql": {utf32Text, "wide32_table", "HR/WIDE32_TABLE#", true}}
	if len(idx.Documents) != len(tests) {
		t.Fatalf("got %d documents", len(idx.Documents))
	}
	for _, doc := range idx.Documents {
		tc, ok := tests[doc.RelativePath]
		if !ok {
			t.Fatalf("unexpected %s", doc.RelativePath)
		}
		if doc.PositionEncoding != scip.PositionEncoding_UTF8CodeUnitOffsetFromLineStart {
			t.Errorf("position encoding: %v", doc.PositionEncoding)
		}
		if tc.embedded && doc.Text != tc.text {
			t.Errorf("decoded %s: %q want %q", doc.RelativePath, doc.Text, tc.text)
		}
		if !tc.embedded && doc.Text != "" {
			t.Errorf("unexpected embedded text: %s", doc.RelativePath)
		}
		found := false
		for _, o := range doc.Occurrences {
			for _, d := range o.Diagnostics {
				t.Errorf("unexpected diagnostic in %s: %v", doc.RelativePath, d)
			}
			if o.Symbol == "scip-plsql oracle demo 1 "+tc.symbol {
				found = true
				want := int32(strings.Index(tc.text, tc.token))
				if o.Range[0] != 0 || o.Range[1] != want || o.Range[2] != want+int32(len(tc.token)) {
					t.Errorf("%s range %v, want byte column %d", doc.RelativePath, o.Range, want)
				}
			}
		}
		if !found {
			t.Errorf("missing definition in %s", doc.RelativePath)
		}
		original, err := os.ReadFile(filepath.Join(root, doc.RelativePath))
		if err != nil || !bytes.Equal(original, files[doc.RelativePath]) {
			t.Errorf("source modified: %s", doc.RelativePath)
		}
	}
}
func TestLegacyOverrideAndBOMMetadata(t *testing.T) {
	// 0xA4 is a currency sign in CP1252 and a euro sign in Latin-9. The
	// ambiguity cannot be resolved from bytes alone, so explicit selection wins.
	data := []byte{0xa4}
	a, err := decodeSource(data, "windows-1252")
	if err != nil {
		t.Fatal(err)
	}
	b, err := decodeSource(data, "iso-8859-15")
	if err != nil {
		t.Fatal(err)
	}
	if a.text != "¤" || b.text != "€" {
		t.Fatalf("explicit decoders: %q %q", a.text, b.text)
	}
	root := t.TempDir()
	text := "CREATE TABLE hr.wide (id NUMBER);\n"
	for name, enc := range map[string]textunicode.Endianness{"le.sql": textunicode.LittleEndian, "be.sql": textunicode.BigEndian} {
		if err := os.WriteFile(filepath.Join(root, name), encodeFixture(t, textunicode.UTF16(enc, textunicode.UseBOM), text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	out := filepath.Join(t.TempDir(), "index.scip")
	if err := Index(Options{Root: root, Output: out, Database: "demo", Version: "1", Schema: "HR"}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	var idx scip.Index
	if err := proto.Unmarshal(raw, &idx); err != nil {
		t.Fatal(err)
	}
	if idx.Metadata.TextDocumentEncoding != scip.TextEncoding_UTF16 {
		t.Fatalf("UTF16 metadata: %v", idx.Metadata.TextDocumentEncoding)
	}
	for _, d := range idx.Documents {
		if !utf8.ValidString(d.Text) || d.Text != text {
			t.Errorf("decoded content %s: %q", d.RelativePath, d.Text)
		}
	}
}
func FuzzDecodeSource(f *testing.F) {
	f.Add([]byte("CREATE TABLE t (x NUMBER);"))
	f.Add([]byte{0xff, 0xfe, 0x41})
	f.Fuzz(func(t *testing.T, data []byte) {
		for _, enc := range []string{"auto", "windows-1252", "iso-8859-15"} {
			decoded, err := decodeSource(data, enc)
			if err == nil && !utf8.ValidString(decoded.text) {
				t.Fatal("invalid UTF-8 after decoding")
			}
		}
	})
}

func TestMalformedUTF16RetainsDocumentAndReportsReplacement(t *testing.T) {
	root := t.TempDir()
	broken := []byte{0xff, 0xfe, 0x41}
	if err := os.WriteFile(filepath.Join(root, "bad.sql"), broken, 0600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "index.scip")
	if err := Index(Options{Root: root, Output: out, Database: "demo", Version: "1", Schema: "HR"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	var idx scip.Index
	if err := proto.Unmarshal(data, &idx); err != nil {
		t.Fatal(err)
	}
	if len(idx.Documents) != 1 || idx.Documents[0].Text != "�" {
		t.Fatalf("missing decoded document: %v", idx.Documents)
	}
	found := false
	for _, o := range idx.Documents[0].Occurrences {
		for _, d := range o.Diagnostics {
			if d.Code == "encoding-replacement" {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("missing decoding diagnostic")
	}
}
func TestInvalidLegacyEncodingRejected(t *testing.T) {
	if _, err := normalizeLegacyEncoding("latin-8"); err == nil {
		t.Fatal("accepted unknown legacy encoding")
	}
}

func TestEncodingChoiceInvalidatesCache(t *testing.T) {
	root := t.TempDir()
	raw := append([]byte("/* "), 0xa4)
	raw = append(raw, []byte(" */ CREATE TABLE hr.emp (id NUMBER);\n")...)
	if err := os.WriteFile(filepath.Join(root, "a.sql"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "index.scip")
	var stats CacheStats
	opts := Options{Root: root, Output: out, Database: "demo", Version: "1", Schema: "HR", CacheDir: filepath.Join(t.TempDir(), "cache"), CacheStats: &stats, LegacyEncoding: "windows-1252"}
	if err := Index(opts); err != nil {
		t.Fatal(err)
	}
	a, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	opts.LegacyEncoding = "iso-8859-15"
	if err := Index(opts); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Hits != 0 || stats.Misses != 1 || bytes.Equal(a, b) {
		t.Fatalf("encoding change reused stale document: %+v", stats)
	}
	var idx scip.Index
	if err := proto.Unmarshal(b, &idx); err != nil {
		t.Fatal(err)
	}
	if len(idx.Documents) != 1 || !strings.Contains(idx.Documents[0].Text, "€") {
		t.Fatal("missing Latin-9 decoded text")
	}
}
