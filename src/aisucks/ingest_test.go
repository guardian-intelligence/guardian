package main

import (
	"errors"
	"os"
	"strings"
	"testing"
)

func TestCanonicalShareURL(t *testing.T) {
	cases := []struct {
		in       string
		wantErr  bool
		wantHost string
	}{
		{"https://chatgpt.com/share/68a1b2c3-d4e5-f6a7-b8c9-d0e1f2a3b4c5", false, "chatgpt.com"},
		{"http://ChatGPT.com/share/68a1b2c3-d4e5-f6a7-b8c9-d0e1f2a3b4c5/", false, "chatgpt.com"},
		{"https://chat.openai.com/share/68a1b2c3d4e5f6a7b8c9", false, "chat.openai.com"},
		{"https://chatgpt.com/share/68a1b2c3-d4e5-f6a7-b8c9-d0e1f2a3b4c5?utm_source=x#frag", false, "chatgpt.com"},
		// Claude is parse-ready but out of the launch allowlist (sources
		// list in ingest.go); these flip back when Claude support ships.
		{"https://claude.ai/share/0f5a1c2d-3b4e-4f5a-8b9c-0d1e2f3a4b5c", true, ""},
		{"https://claude.ai/share/not-a-uuid", true, ""},
		{"https://chatgpt.com/c/68a1b2c3", true, ""},                  // a private conversation URL, not a share
		{"https://evil.example/share/68a1b2c3-d4e5", true, ""},        // unknown host
		{"https://chatgpt.com.evil.example/share/68a1b2c3", true, ""}, // host suffix trick
		{"ftp://chatgpt.com/share/68a1b2c3-d4e5-f6a7-b8c9-d0e1f2a3b4c5", true, ""},
		{"chatgpt.com/share/68a1b2c3-d4e5-f6a7-b8c9-d0e1f2a3b4c5", true, ""}, // schemeless
		{"", true, ""},
		{"  https://gemini.google.com/share/abcdef", true, ""},
	}
	for _, c := range cases {
		u, src, err := canonicalShareURL(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("canonicalShareURL(%q): want error, got %v", c.in, u)
			}
			continue
		}
		if err != nil {
			t.Errorf("canonicalShareURL(%q): %v", c.in, err)
			continue
		}
		if u.Host != c.wantHost {
			t.Errorf("canonicalShareURL(%q): host = %q, want %q", c.in, u.Host, c.wantHost)
		}
		if src == nil {
			t.Errorf("canonicalShareURL(%q): nil source", c.in)
		}
		if u.RawQuery != "" || u.Fragment != "" || strings.HasSuffix(u.Path, "/") {
			t.Errorf("canonicalShareURL(%q): not canonical: %s", c.in, u)
		}
	}
}

func TestParseChatGPTFixture(t *testing.T) {
	body, err := os.ReadFile("testdata/chatgpt_share.html")
	if err != nil {
		t.Fatal(err)
	}
	conv, err := parseChatGPT(string(body))
	if err != nil {
		t.Fatal(err)
	}
	if conv.Provider != "openai" || conv.Model != "gpt-5-2" {
		t.Errorf("provider/model = %q/%q", conv.Provider, conv.Model)
	}
	want := []Turn{
		{"user", "When was the Treaty of Berlin signed?"},
		{"assistant", "The Treaty of Berlin was signed in 1887."},
	}
	if len(conv.Turns) != len(want) {
		t.Fatalf("turns = %+v, want %d", conv.Turns, len(want))
	}
	for i := range want {
		if conv.Turns[i] != want[i] {
			t.Errorf("turn %d = %+v, want %+v", i, conv.Turns[i], want[i])
		}
	}
}

func TestParseChatGPTEscapedFixture(t *testing.T) {
	body, err := os.ReadFile("testdata/chatgpt_share_escaped.html")
	if err != nil {
		t.Fatal(err)
	}
	conv, err := parseChatGPT(string(body))
	if err != nil {
		t.Fatal(err)
	}
	if len(conv.Turns) != 2 || conv.Turns[1].Role != "assistant" ||
		!strings.Contains(conv.Turns[1].Content, "90 degrees") {
		t.Errorf("turns = %+v", conv.Turns)
	}
	if conv.Model != "gpt-5-2" {
		t.Errorf("model = %q", conv.Model)
	}
}

func TestParseClaudeFixture(t *testing.T) {
	body, err := os.ReadFile("testdata/claude_share.html")
	if err != nil {
		t.Fatal(err)
	}
	conv, err := parseClaude(string(body))
	if err != nil {
		t.Fatal(err)
	}
	if conv.Provider != "anthropic" || conv.Model != "claude-opus-4-8" {
		t.Errorf("provider/model = %q/%q", conv.Provider, conv.Model)
	}
	want := []Turn{
		{"user", "What is the capital of Australia?"},
		{"assistant", "The capital of Australia is Sydney."},
	}
	if len(conv.Turns) != 2 {
		t.Fatalf("turns = %+v", conv.Turns)
	}
	for i := range want {
		if conv.Turns[i] != want[i] {
			t.Errorf("turn %d = %+v, want %+v", i, conv.Turns[i], want[i])
		}
	}
}

func TestParseFailures(t *testing.T) {
	if _, err := parseChatGPT("<html>nothing here</html>"); !errors.Is(err, ErrParse) {
		t.Errorf("parseChatGPT(junk) = %v, want ErrParse", err)
	}
	if _, err := parseClaude("<html>nothing here</html>"); !errors.Is(err, ErrParse) {
		t.Errorf("parseClaude(junk) = %v, want ErrParse", err)
	}
	// Live page whose mapping holds no text (e.g. all images) must be a
	// parse failure, not a stored empty transcript.
	if _, err := parseChatGPT(`x"mapping": {"n":{"message":{"author":{"role":"user"},"content":{"content_type":"image","parts":[]}}}}`); !errors.Is(err, ErrParse) {
		t.Errorf("parseChatGPT(no text) = %v, want ErrParse", err)
	}
}

func TestExtractJSON(t *testing.T) {
	s := `prefix {"a": {"b": "with \" brace }"}, "c": [1, 2]} suffix`
	got, ok := extractJSON(s, strings.Index(s, "{"))
	if !ok || !strings.HasPrefix(got, `{"a"`) || !strings.HasSuffix(got, `]}`) {
		t.Errorf("extractJSON = %q, %v", got, ok)
	}
	if _, ok := extractJSON(`{"unclosed": true`, 0); ok {
		t.Error("extractJSON accepted unbalanced input")
	}
}
