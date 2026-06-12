package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
)

// chatgpt_share_flight.html is a trimmed copy of a live chatgpt.com share
// page using the React Router 7 / turbo-stream framing: the conversation is
// a flat array of indexed values streamed via
// window.__reactRouterContext.streamController.enqueue, plus a deferred
// promise-resolution chunk (`P451:[{}]`) and a statsig decoy blob containing
// "old_to_new_mapping".
func TestParseChatGPTFlightFixture(t *testing.T) {
	body, err := os.ReadFile("testdata/chatgpt_share_flight.html")
	if err != nil {
		t.Fatal(err)
	}
	conv, err := parseChatGPT(string(body))
	if err != nil {
		t.Fatal(err)
	}
	if conv.Provider != "openai" || conv.Model != "gpt-5-5-thinking" {
		t.Errorf("provider/model = %q/%q", conv.Provider, conv.Model)
	}
	// The visible conversation is one user turn (multimodal: image + text)
	// and one assistant turn; hidden custom-instruction stubs and assistant
	// "thoughts" nodes must not leak in.
	if len(conv.Turns) != 2 {
		t.Fatalf("turns = %+v, want 2", conv.Turns)
	}
	if conv.Turns[0].Role != "user" || conv.Turns[0].Content != "Is this considered a six pack? " {
		t.Errorf("turn 0 = %+v", conv.Turns[0])
	}
	if conv.Turns[1].Role != "assistant" ||
		!strings.Contains(conv.Turns[1].Content, "visible abs / basically a six-pack") {
		t.Errorf("turn 1 = %+v", conv.Turns[1])
	}
}

func TestParseChatGPTFlightMisses(t *testing.T) {
	cases := map[string]string{
		"promise frame only":       `<script>window.__reactRouterContext.streamController.enqueue("P451:[{}]\n");</script>`,
		"array without share data": `<script>window.__reactRouterContext.streamController.enqueue("[{\"_1\":2},\"other\",\"x\"]\n");</script>`,
		"unterminated literal":     `<script>window.__reactRouterContext.streamController.enqueue("[1,2`,
		"negative sentinel root":   `<script>window.__reactRouterContext.streamController.enqueue("[{\"_1\":-5},\"loaderData\"]\n");</script>`,
		"reference out of range":   `<script>window.__reactRouterContext.streamController.enqueue("[{\"_1\":999},\"loaderData\"]\n");</script>`,
	}
	for name, body := range cases {
		if _, err := parseChatGPT(body); !errors.Is(err, ErrParse) {
			t.Errorf("%s: err = %v, want ErrParse", name, err)
		}
	}
}

// A linear back-reference chain (arr[i] = [i+1]) makes resolve recurse once
// per element; past the runtime stack limit that is a fatal, unrecoverable
// stack overflow. flightMaxDepth must convert such chains into ErrParse. The
// chain here is deep enough to trip the guard but far below the ~1.2M depth
// that crashes an unguarded resolve, so a regression hangs the assertion,
// not the process.
func TestParseChatGPTFlightDeepReferenceChain(t *testing.T) {
	n := flightMaxDepth + 10
	var b strings.Builder
	b.WriteString(`<script>window.__reactRouterContext.streamController.enqueue("[`)
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "[%d],", i)
	}
	b.WriteString(`0]\n");</script>`)
	if _, err := parseChatGPT(b.String()); !errors.Is(err, ErrParse) {
		t.Errorf("err = %v, want ErrParse", err)
	}
}
