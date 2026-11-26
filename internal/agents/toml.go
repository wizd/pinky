package agents

import (
	"bytes"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strconv"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// This file changes one table of a TOML settings file, such as Codex's
// [mcp_servers.pinky], line by line, leaving the rest of the file as it
// was. As with JSON, the change is checked by reading the file before and
// after it.

// utf8BOM is the byte order mark some Windows editors put at the start of
// a file. Codex reads past it, as Pinky does for JSON settings.
const utf8BOM = "\xef\xbb\xbf"

// cutBOM returns src without its byte order mark, and the mark it had.
func cutBOM(src []byte) (bom string, rest []byte) {
	if rest, ok := bytes.CutPrefix(src, []byte(utf8BOM)); ok {
		return utf8BOM, rest
	}
	return "", src
}

// readTOML reads a TOML file into maps; an empty file is an empty table.
func readTOML(src []byte) (map[string]any, error) {
	_, src = cutBOM(src)
	return parseTOML(src)
}

// parseTOML reads TOML whose byte order mark was cut: a second mark is
// not valid, as Codex finds too.
func parseTOML(src []byte) (map[string]any, error) {
	v := map[string]any{}
	if err := toml.Unmarshal(src, &v); err != nil {
		var de *toml.DecodeError
		if errors.As(err, &de) {
			row, col := de.Position()
			return nil, fmt.Errorf("not valid TOML at line %d, column %d: %s", row, col, strings.TrimPrefix(de.Error(), "toml: "))
		}
		return nil, fmt.Errorf("not valid TOML: %w", err)
	}
	return v, nil
}

// bareKey is a key that needs no quotes.
var bareKey = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// tomlArrayHeader matches the header of an array of tables, [[a.b]].
var tomlArrayHeader = regexp.MustCompile(`^\s*\[\[`)

// parseHeaderKey reads a standard table header [a.b."c#d"] (# comment),
// keeping quoted '#', '[' and ']' inside keys intact.
func parseHeaderKey(line string) ([]string, bool) {
	s := strings.TrimSpace(strings.TrimRight(line, "\r\n"))
	if !strings.HasPrefix(s, "[") || strings.HasPrefix(s, "[[") {
		return nil, false
	}
	inQuote := byte(0)
	closeIdx := -1
	for i := 1; i < len(s); i++ {
		c := s[i]
		if inQuote != 0 {
			if inQuote == '"' && c == '\\' {
				i++
				continue
			}
			if c == inQuote {
				inQuote = 0
			}
			continue
		}
		if c == '"' || c == '\'' {
			inQuote = c
			continue
		}
		if c == ']' {
			closeIdx = i
			break
		}
	}
	if closeIdx < 0 {
		return nil, false
	}
	tail := strings.TrimSpace(s[closeIdx+1:])
	if tail != "" && !strings.HasPrefix(tail, "#") {
		return nil, false
	}
	return splitKey(s[1:closeIdx])
}

// splitKey reads a dotted key, a.b."c d", into its parts.
func splitKey(s string) ([]string, bool) {
	var parts []string
	for s = strings.TrimSpace(s); s != ""; {
		var part string
		switch s[0] {
		case '"':
			end := 1
			for end < len(s) && s[end] != '"' {
				if s[end] == '\\' {
					end++
				}
				end++
			}
			if end >= len(s) {
				return nil, false
			}
			p, err := strconv.Unquote(s[:end+1])
			if err != nil {
				return nil, false
			}
			part, s = p, s[end+1:]
		case '\'':
			end := strings.IndexByte(s[1:], '\'')
			if end < 0 {
				return nil, false
			}
			part, s = s[1:end+1], s[end+2:]
		default:
			end := strings.IndexByte(s, '.')
			if end < 0 {
				end = len(s)
			}
			part, s = strings.TrimSpace(s[:end]), s[end:]
			if !bareKey.MatchString(part) {
				return nil, false
			}
		}
		parts = append(parts, part)
		s = strings.TrimSpace(s)
		if s == "" {
			break
		}
		if s[0] != '.' {
			return nil, false
		}
		s = strings.TrimSpace(s[1:])
	}
	return parts, len(parts) > 0
}

func hasPrefix(key, prefix []string) bool {
	if len(key) < len(prefix) {
		return false
	}
	for i := range prefix {
		if key[i] != prefix[i] {
			return false
		}
	}
	return true
}

// tomlLines splits src into lines that keep their line ends, and marks the
// ones inside a multi-line string, which are never headers.
func tomlLines(src []byte) (lines []string, inString []bool) {
	quote := "" // `"""` or `'''` when a multi-line string spans across lines
	for len(src) > 0 {
		i := bytes.IndexByte(src, '\n')
		var line string
		if i < 0 {
			line, src = string(src), nil
		} else {
			line, src = string(src[:i+1]), src[i+1:]
		}
		lines = append(lines, line)
		inString = append(inString, quote != "")
		// Scan strings and comments left-to-right so single-line strings holding
		// ''' / """ / # or escaped quotes inside """...""" never confuse state.
		for j := 0; j < len(line); {
			if quote == `"""` {
				if line[j] == '\\' {
					j += 2
					continue
				}
				if strings.HasPrefix(line[j:], `"""`) {
					// Up to two extra quotes may precede the closing """ (e.g. """a"""").
					run := 3
					for j+run < len(line) && line[j+run] == '"' && run < 5 {
						run++
					}
					quote = ""
					j += run
					continue
				}
				j++
				continue
			}
			if quote == `'''` {
				if strings.HasPrefix(line[j:], `'''`) {
					run := 3
					for j+run < len(line) && line[j+run] == '\'' && run < 5 {
						run++
					}
					quote = ""
					j += run
					continue
				}
				j++
				continue
			}
			c := line[j]
			if c == '#' {
				break // comment to end of line
			}
			if strings.HasPrefix(line[j:], `"""`) {
				quote = `"""`
				j += 3
				continue
			}
			if strings.HasPrefix(line[j:], `'''`) {
				quote = `'''`
				j += 3
				continue
			}
			if c == '"' {
				j++
				for j < len(line) && line[j] != '"' && line[j] != '\n' {
					if line[j] == '\\' {
						j++
					}
					j++
				}
				if j < len(line) && line[j] == '"' {
					j++
				}
				continue
			}
			if c == '\'' {
				j++
				for j < len(line) && line[j] != '\'' && line[j] != '\n' {
					j++
				}
				if j < len(line) && line[j] == '\'' {
					j++
				}
				continue
			}
			j++
		}
	}
	return lines, inString
}

// tomlTable finds the lines of the table key and its sub-tables: a list of
// [start, end) line ranges.
func tomlTable(lines []string, inString []bool, key []string) [][2]int {
	var spans [][2]int
	open := -1
	for i, line := range lines {
		if inString[i] {
			continue
		}
		header := tomlArrayHeader.MatchString(line)
		var k []string
		if !header {
			if parts, ok := parseHeaderKey(line); ok {
				k, header = parts, true
			}
		}
		if !header {
			continue
		}
		ours := k != nil && hasPrefix(k, key)
		if open >= 0 {
			end := i
			if !ours {
				end = tomlTableEnd(lines, inString, open, i)
			}
			spans = append(spans, [2]int{open, end})
			open = -1
		}
		if ours {
			open = i
		}
	}
	if open >= 0 {
		spans = append(spans, [2]int{open, tomlTableEnd(lines, inString, open, len(lines))})
	}
	return spans
}

// tomlTableEnd is where the table whose header is at open ends, when what
// comes at next is not part of it. Comments right under its last line (or
// its header) are its own and go with it, unless they run right up to the
// next table's header, which they are about. After a blank line, comments
// are about what follows (the next table, a table commented out, or
// nothing) and stay.
func tomlTableEnd(lines []string, inString []bool, open, next int) int {
	comment := func(i int) bool { return strings.HasPrefix(strings.TrimSpace(lines[i]), "#") }
	last := next // right after the table's last line that is not a comment or blank
	for last-1 > open && !inString[last-1] && (comment(last-1) || strings.TrimSpace(lines[last-1]) == "") {
		last--
	}
	j := last
	for j < next && comment(j) {
		j++
	}
	if j == next && next < len(lines) {
		return last // comments right over the next header
	}
	for ; j < next; j++ {
		if comment(j) {
			return j
		}
	}
	return next
}

// tomlString writes s as a TOML basic string.
func tomlString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\t':
			b.WriteString(`\t`)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, `\u%04X`, r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// tomlServer is an MCP server's table in Codex's settings.
func tomlServer(key []string, srv Server, nl string) string {
	parts := make([]string, len(key))
	for i, k := range key {
		parts[i] = k
		if !bareKey.MatchString(k) {
			parts[i] = tomlString(k)
		}
	}
	args := make([]string, len(srv.Args))
	for i, a := range srv.Args {
		args[i] = tomlString(a)
	}
	return "[" + strings.Join(parts, ".") + "]" + nl +
		"command = " + tomlString(srv.Command) + nl +
		"args = [" + strings.Join(args, ", ") + "]" + nl
}

// setTOMLServer returns src with the table key (as mcp_servers.pinky) set
// to srv, in place of the one there or at the end of the file.
func setTOMLServer(src []byte, key []string, srv Server) ([]byte, error) {
	bom, src := cutBOM(src)
	before, err := parseTOML(src)
	if err != nil {
		return nil, err
	}
	nl := "\n"
	if bytes.Contains(src, []byte("\r\n")) {
		nl = "\r\n"
	}
	lines, inString := tomlLines(src)
	spans := tomlTable(lines, inString, key)
	if _, ok := lookup(before, key...); ok && len(spans) == 0 {
		return nil, fmt.Errorf("[%s] is written in a way Pinky does not change; change it by hand", strings.Join(key, "."))
	}
	table := tomlServer(key, srv, nl)
	var out strings.Builder
	if len(spans) == 0 {
		out.Write(src)
		if len(src) > 0 {
			if !bytes.HasSuffix(src, []byte("\n")) {
				out.WriteString(nl)
			}
			out.WriteString(nl)
		}
		out.WriteString(table)
	} else {
		// The new table takes the place of the first old one; a blank line
		// keeps it apart from what follows.
		skip := make([]bool, len(lines))
		for _, sp := range spans {
			for i := sp[0]; i < sp[1]; i++ {
				skip[i] = true
			}
		}
		for i, line := range lines {
			if i == spans[0][0] {
				out.WriteString(table)
				if spans[0][1] < len(lines) {
					out.WriteString(nl)
				}
			}
			if !skip[i] {
				out.WriteString(line)
			}
		}
	}
	res := []byte(out.String())
	after, err := parseTOML(res)
	if err != nil {
		return nil, fmt.Errorf("the change would break the file: %w", err)
	}
	want := map[string]any{"command": srv.Command, "args": toAny(srv.Args)}
	if got, ok := lookup(after, key...); !ok || !reflect.DeepEqual(got, want) {
		return nil, errors.New("the change did not read back")
	}
	if !reflect.DeepEqual(without(after, key[:len(key)-1], key[len(key)-1], before), without(before, key[:len(key)-1], key[len(key)-1], before)) {
		return nil, errors.New("the change would alter more than Pinky's entry")
	}
	return append([]byte(bom), res...), nil
}

// deleteTOMLServer returns src without the table key and its sub-tables;
// changed is false when there was none.
func deleteTOMLServer(src []byte, key []string) (out []byte, changed bool, err error) {
	orig := src
	bom, src := cutBOM(src)
	before, err := parseTOML(src)
	if err != nil {
		return nil, false, err
	}
	if _, ok := lookup(before, key...); !ok {
		return orig, false, nil
	}
	lines, inString := tomlLines(src)
	spans := tomlTable(lines, inString, key)
	if len(spans) == 0 {
		return nil, false, fmt.Errorf("[%s] is written in a way Pinky does not change; remove it by hand", strings.Join(key, "."))
	}
	var b strings.Builder
	for i, line := range lines {
		skip := false
		for _, s := range spans {
			if i >= s[0] && i < s[1] {
				skip = true
			}
		}
		if !skip {
			b.WriteString(line)
		}
	}
	res := []byte(b.String())
	// The blank line that kept the table apart goes with it.
	res = bytes.TrimRight(res, "\r\n")
	if len(res) > 0 {
		nl := "\n"
		if bytes.Contains(src, []byte("\r\n")) {
			nl = "\r\n"
		}
		res = append(res, nl...)
	}
	after, err := parseTOML(res)
	if err != nil {
		return nil, false, fmt.Errorf("the change would break the file: %w", err)
	}
	if _, ok := lookup(after, key...); ok {
		return nil, false, errors.New("the entry is still there")
	}
	// A table made only to hold Pinky's (mcp_servers) goes with it.
	path := key[:len(key)-1]
	if !reflect.DeepEqual(pruneEmpty(after, path), pruneEmpty(without(before, path, key[len(key)-1], before), path)) {
		return nil, false, errors.New("the change would alter more than Pinky's entry")
	}
	return append([]byte(bom), res...), true, nil
}

func toAny(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}
