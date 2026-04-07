package extract

import (
	"context"
	"os"
	"strings"
	"unicode/utf8"
)

type RTFExtractor struct{}

func (r *RTFExtractor) Extract(_ context.Context, path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return extractRTFText(string(data)), nil
}

func (r *RTFExtractor) Supports(path string) bool {
	return strings.EqualFold(ext(path), ".rtf")
}

// extractRTFText extracts plain text from RTF content by parsing control words
// and stripping formatting. Handles unicode escapes, paragraph breaks, and
// common control words.
func extractRTFText(rtf string) string {
	var buf strings.Builder
	depth := 0
	skipGroup := 0
	i := 0

	// Skip groups that contain metadata/formatting, not visible text.
	skipGroupWords := map[string]bool{
		"fonttbl": true, "colortbl": true, "stylesheet": true,
		"info": true, "header": true, "footer": true,
		"headerl": true, "headerr": true, "footerl": true, "footerr": true,
		"pict": true, "object": true, "fldinst": true,
		"*": true,
	}

	for i < len(rtf) {
		ch := rtf[i]

		switch ch {
		case '{':
			depth++
			i++
		case '}':
			if skipGroup > 0 && depth == skipGroup {
				skipGroup = 0
			}
			depth--
			i++
		case '\\':
			if i+1 >= len(rtf) {
				i++
				continue
			}
			next := rtf[i+1]

			// Escaped characters.
			switch next {
			case '{', '}', '\\':
				if skipGroup == 0 {
					buf.WriteByte(next)
				}
				i += 2
				continue
			case '\'':
				// Hex-encoded character: \'xx
				if i+3 < len(rtf) {
					hexStr := rtf[i+2 : i+4]
					val := hexToByte(hexStr)
					if skipGroup == 0 && val > 0 {
						buf.WriteByte(val)
					}
					i += 4
				} else {
					i += 2
				}
				continue
			case '\n', '\r':
				// Line break escape.
				if skipGroup == 0 {
					buf.WriteByte('\n')
				}
				i += 2
				continue
			}

			// Parse control word.
			word, param, end := parseRTFControlWord(rtf, i+1)
			i = end

			if skipGroup > 0 {
				continue
			}

			if skipGroupWords[word] {
				skipGroup = depth
				continue
			}

			switch word {
			case "par", "line":
				buf.WriteByte('\n')
			case "tab":
				buf.WriteByte('\t')
			case "u":
				// Unicode character: \uN followed by an ANSI replacement character.
				if param != 0 {
					var r rune
					if param < 0 {
						r = rune(param + 65536)
					} else {
						r = rune(param)
					}
					if utf8.ValidRune(r) {
						buf.WriteRune(r)
					}
					// Skip the ANSI replacement character that follows \uN.
					if i < len(rtf) && rtf[i] != '{' && rtf[i] != '}' && rtf[i] != '\\' {
						i++
					}
				}
			case "lquote":
				buf.WriteRune('\u2018')
			case "rquote":
				buf.WriteRune('\u2019')
			case "ldblquote":
				buf.WriteRune('\u201C')
			case "rdblquote":
				buf.WriteRune('\u201D')
			case "endash":
				buf.WriteRune('\u2013')
			case "emdash":
				buf.WriteRune('\u2014')
			case "bullet":
				buf.WriteRune('\u2022')
			}
		default:
			if skipGroup == 0 {
				buf.WriteByte(ch)
			}
			i++
		}
	}

	return cleanSpacing(buf.String())
}

// parseRTFControlWord extracts a control word and optional numeric parameter
// starting at pos (which should be the first character after the backslash).
func parseRTFControlWord(rtf string, pos int) (word string, param int, end int) {
	i := pos

	// Read alphabetic control word.
	wordStart := i
	for i < len(rtf) && rtf[i] >= 'a' && rtf[i] <= 'z' {
		i++
	}
	word = rtf[wordStart:i]

	// Read optional numeric parameter (possibly negative).
	param = 0
	hasParam := false
	neg := false
	if i < len(rtf) && rtf[i] == '-' {
		neg = true
		i++
	}
	for i < len(rtf) && rtf[i] >= '0' && rtf[i] <= '9' {
		param = param*10 + int(rtf[i]-'0')
		hasParam = true
		i++
	}
	if neg && hasParam {
		param = -param
	}

	// Skip optional trailing space delimiter.
	if i < len(rtf) && rtf[i] == ' ' {
		i++
	}

	return word, param, i
}

func hexToByte(s string) byte {
	if len(s) != 2 {
		return 0
	}
	return hexDigit(s[0])<<4 | hexDigit(s[1])
}

func hexDigit(c byte) byte {
	switch {
	case c >= '0' && c <= '9':
		return c - '0'
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10
	default:
		return 0
	}
}
