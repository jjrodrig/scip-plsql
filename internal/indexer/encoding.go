package indexer

import (
	"bytes"
	"fmt"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/charmap"
	textunicode "golang.org/x/text/encoding/unicode"
	"golang.org/x/text/encoding/unicode/utf32"
)

type sourceEncoding uint8

const (
	encodingUTF8 sourceEncoding = iota
	encodingUTF16
	encodingOther
)

type decodedSource struct {
	text     string
	encoding sourceEncoding
	embedded bool
	utf8BOM  bool
}

func normalizeLegacyEncoding(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "auto":
		return "auto", nil
	case "windows-1252", "cp1252":
		return "windows-1252", nil
	case "iso-8859-15", "latin9":
		return "iso-8859-15", nil
	default:
		return "", fmt.Errorf("codificación heredada desconocida %q", value)
	}
}

// guessLegacyEncoding cannot distinguish the two encodings in general. Prefer
// Windows-1252, except for bytes strongly associated with ISO-8859-15; callers
// with a known corpus should explicitly configure the encoding.
func guessLegacyEncoding(data []byte) string {
	for _, b := range data {
		if b >= 0x80 && b <= 0x9f {
			return "windows-1252"
		}
	}
	for _, b := range data {
		switch b {
		case 0xa4, 0xbc, 0xbd, 0xbe:
			return "iso-8859-15"
		}
	}
	return "windows-1252"
}
func decodeSource(data []byte, legacy string) (decodedSource, error) {
	var enc encoding.Encoding
	var kind sourceEncoding
	switch {
	case bytes.HasPrefix(data, []byte{0, 0, 0xfe, 0xff}):
		enc = utf32.UTF32(utf32.BigEndian, utf32.UseBOM)
		kind = encodingOther
	case bytes.HasPrefix(data, []byte{0xff, 0xfe, 0, 0}):
		enc = utf32.UTF32(utf32.LittleEndian, utf32.UseBOM)
		kind = encodingOther
	case bytes.HasPrefix(data, []byte{0xfe, 0xff}):
		enc = textunicode.UTF16(textunicode.BigEndian, textunicode.UseBOM)
		kind = encodingUTF16
	case bytes.HasPrefix(data, []byte{0xff, 0xfe}):
		enc = textunicode.UTF16(textunicode.LittleEndian, textunicode.UseBOM)
		kind = encodingUTF16
	default:
		if utf8.Valid(data) {
			return decodedSource{text: string(data), encoding: encodingUTF8, utf8BOM: bytes.HasPrefix(data, []byte{0xef, 0xbb, 0xbf})}, nil
		}
		if legacy == "auto" {
			legacy = guessLegacyEncoding(data)
		}
		switch legacy {
		case "windows-1252":
			enc = charmap.Windows1252
		case "iso-8859-15":
			enc = charmap.ISO8859_15
		default:
			return decodedSource{}, fmt.Errorf("codificación heredada desconocida %q", legacy)
		}
		kind = encodingOther
	}
	decoded, err := enc.NewDecoder().Bytes(data)
	if err != nil {
		return decodedSource{}, err
	}
	if !utf8.Valid(decoded) {
		return decodedSource{}, fmt.Errorf("la transcodificación no produjo UTF-8 válido")
	}
	return decodedSource{text: string(decoded), encoding: kind, embedded: true}, nil
}
