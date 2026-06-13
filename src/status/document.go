package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html"
	"strings"

	"github.com/BurntSushi/toml"
	"gopkg.in/yaml.v3"
)

// The document model: ordered sections of key/value entries. TOML is the
// product surface and carries the honesty comments; JSON and YAML are the
// same values re-encoded (comments are presentation, not data — the three
// encodings parse to identical value trees, pinned by TestEncodingsAgree).
//
// Values are drawn from the closed set string|bool|int64|float64 so every
// encoder agrees on the value space.

// Entry is one key in a section. A non-empty Comment renders as a `# ` line
// above the key in the TOML (and therefore on the HTML page).
type Entry struct {
	Key     string
	Value   any
	Comment string
}

// Section is one TOML table; Name is the dotted table path (e.g. "fleet.dev").
type Section struct {
	Name     string
	Comments []string
	Entries  []Entry
}

// Document is the whole page. Header lines render as leading TOML comments.
type Document struct {
	Header   []string
	Sections []Section
}

// TOML renders the document. Section layout and comments are ours (no TOML
// encoder emits comments, and the comments are product copy); each key/value
// line is emitted by the BurntSushi encoder so quoting, escaping, and float
// formatting follow the library, not hand-rolled grammar. The golden test
// re-parses the output with the same library.
func (d Document) TOML() ([]byte, error) {
	var b bytes.Buffer
	for _, line := range d.Header {
		fmt.Fprintf(&b, "# %s\n", line)
	}
	for _, s := range d.Sections {
		fmt.Fprintf(&b, "\n[%s]\n", s.Name)
		for _, c := range s.Comments {
			fmt.Fprintf(&b, "# %s\n", c)
		}
		for _, e := range s.Entries {
			if e.Comment != "" {
				fmt.Fprintf(&b, "# %s\n", e.Comment)
			}
			line, err := tomlKV(e.Key, e.Value)
			if err != nil {
				return nil, fmt.Errorf("toml %s.%s: %w", s.Name, e.Key, err)
			}
			b.WriteString(line)
		}
	}
	return b.Bytes(), nil
}

// tomlKV encodes a single `key = value` line via the library encoder.
func tomlKV(key string, value any) (string, error) {
	var b bytes.Buffer
	if err := toml.NewEncoder(&b).Encode(map[string]any{key: value}); err != nil {
		return "", err
	}
	return strings.TrimLeft(b.String(), "\n"), nil
}

// tree nests the dotted section names into the map structure JSON and YAML
// encode. Key order inside maps is whatever those encoders do (both sort);
// order is presentation and only the TOML is typeset.
func (d Document) tree() map[string]any {
	root := map[string]any{}
	for _, s := range d.Sections {
		cur := root
		for _, part := range strings.Split(s.Name, ".") {
			next, ok := cur[part].(map[string]any)
			if !ok {
				next = map[string]any{}
				cur[part] = next
			}
			cur = next
		}
		for _, e := range s.Entries {
			cur[e.Key] = e.Value
		}
	}
	return root
}

func (d Document) JSON() ([]byte, error) {
	out, err := json.MarshalIndent(d.tree(), "", "  ")
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}

func (d Document) YAML() ([]byte, error) {
	return yaml.Marshal(d.tree())
}

// pageShell is the entire HTML surface: dark, monospace, one header line, the
// TOML in one <pre>, exactly three plain links. Zero JavaScript, zero
// interactive elements beyond the links — pinned by TestPageHasNoScript.
const pageShell = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>GUARDIAN INTELLIGENCE — fleet status</title>
<style>
body{margin:0;padding:24px 16px 48px;background:#0a0a0a;color:#c9c9c9;font:14px/1.6 ui-monospace,SFMono-Regular,Menlo,Consolas,monospace}
main{max-width:88ch;margin:0 auto}
h1{font:inherit;font-weight:700;color:#4ade80;letter-spacing:.04em;margin:0 0 16px}
pre{white-space:pre-wrap;overflow-wrap:anywhere;margin:0 0 24px}
nav a{color:#4ade80;margin-right:20px}
</style>
</head>
<body>
<main>
<h1>GUARDIAN INTELLIGENCE — fleet status</h1>
<pre>%s</pre>
<nav><a href="/status.toml">status.toml</a><a href="/status.json">status.json</a><a href="/status.yaml">status.yaml</a></nav>
</main>
</body>
</html>
`

func renderHTML(tomlDoc []byte) []byte {
	return fmt.Appendf(nil, pageShell, html.EscapeString(string(tomlDoc)))
}
