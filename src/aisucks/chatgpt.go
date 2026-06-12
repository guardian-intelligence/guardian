package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// chatgptSource extracts transcripts from ChatGPT share pages
// (chatgpt.com/share/<id>, formerly chat.openai.com). The page embeds the
// conversation JSON in script-borne state whose framing changes without
// notice; extraction targets the stable inner shape — the "mapping" object
// of message nodes — rather than any particular framework wrapper, and a
// miss is ErrParse (stored as parse_failed, retried after an adapter fix).
type chatgptSource struct{}

var chatgptPath = regexp.MustCompile(`^/share/[A-Za-z0-9-]{8,64}$`)

func (chatgptSource) Match(u *url.URL) bool {
	if u.Host != "chatgpt.com" && u.Host != "chat.openai.com" {
		return false
	}
	return chatgptPath.MatchString(u.Path)
}

func (chatgptSource) Fetch(ctx context.Context, u *url.URL) (*Conversation, error) {
	body, err := fetchPage(ctx, sourceChatGPT, u)
	if err != nil {
		return nil, err // transport reasons already counted in fetchPage
	}
	conv, err := parseChatGPT(body)
	if err != nil {
		// ChatGPT serves a 200 soft-404 (the app shell, no conversation)
		// for share IDs that don't exist, so fetchPage can't bounce a
		// fabricated-but-well-formed link on status alone. Only a page that
		// actually carries conversation markers is a real conversation worth
		// parking for re-parse after a format change; one without them is
		// effectively dead and must bounce — otherwise a made-up UUID parks
		// as parse_failed and pollutes the corpus.
		if errors.Is(err, ErrParse) && !chatgptHasConversation(body) {
			// The v6-incident signature, now a series of its own: the two
			// causes ErrGone collapses are told apart here, where they fork.
			fetchTotal.WithLabelValues(sourceChatGPT, "no_conversation").Inc()
			return nil, fmt.Errorf("%w: live page carries no conversation", ErrGone)
		}
		fetchTotal.WithLabelValues(sourceChatGPT, "parse").Inc()
		return nil, err
	}
	fetchTotal.WithLabelValues(sourceChatGPT, "ok").Inc()
	return conv, nil
}

// chatgptHasConversation reports whether a fetched share page embeds a
// conversation at all (vs the 200 soft-404 shell). Both markers are present
// on real pages — in the flight and legacy framings alike, escaped or not —
// and absent on the not-found shell.
func chatgptHasConversation(body string) bool {
	return strings.Contains(body, "linear_conversation") ||
		strings.Contains(body, "default_model_slug")
}

// chatgptNode is one entry of the share payload's "mapping" object. The
// shape has been stable across ChatGPT's frontend rewrites even as the
// wrapper around it churned.
type chatgptNode struct {
	Message *struct {
		Author struct {
			Role string `json:"role"`
		} `json:"author"`
		CreateTime float64 `json:"create_time"`
		Content    struct {
			ContentType string `json:"content_type"`
			Parts       []any  `json:"parts"`
		} `json:"content"`
	} `json:"message"`
}

// parseChatGPT tries the legacy mapping framings, then the React Router 7
// flight framing. Exactly one parseTotal increment per call — the winning
// strategy, or strategy="none" on a total miss; a shift between strategies
// is the format-drift signal the counter exists to catch.
func parseChatGPT(body string) (*Conversation, error) {
	conv, err := parseChatGPTMapping(body)
	if err == nil {
		parseTotal.WithLabelValues(sourceChatGPT, "mapping", "ok").Inc()
		return conv, nil
	}
	conv, err = parseChatGPTFlight(body)
	if err != nil {
		parseTotal.WithLabelValues(sourceChatGPT, "none", "miss").Inc()
		return nil, err
	}
	parseTotal.WithLabelValues(sourceChatGPT, "flight", "ok").Inc()
	return conv, nil
}

// parseChatGPTMapping handles the legacy framings, where a literal
// `"mapping":` object exists either as plain JSON (__NEXT_DATA__) or
// JSON-escaped inside a JS string.
func parseChatGPTMapping(body string) (*Conversation, error) {
	// The mapping object may appear JSON-escaped inside a JS string
	// (`\"mapping\":`) depending on how the page streams its state; unescape
	// that framing first so one search works for both.
	if !strings.Contains(body, `"mapping":`) && strings.Contains(body, `\"mapping\":`) {
		body = strings.ReplaceAll(body, `\"`, `"`)
		body = strings.ReplaceAll(body, `\\`, `\`)
	}

	conv := &Conversation{Provider: "openai"}
	if m := regexp.MustCompile(`"(?:default_model_slug|model_slug)":\s*"([^"]+)"`).FindStringSubmatch(body); m != nil {
		conv.Model = m[1]
	}

	idx := strings.Index(body, `"mapping":`)
	if idx < 0 {
		return nil, fmt.Errorf("%w: no mapping object", ErrParse)
	}
	start := idx + len(`"mapping":`)
	for start < len(body) && (body[start] == ' ' || body[start] == '\n') {
		start++
	}
	blob, ok := extractJSON(body, start)
	if !ok {
		return nil, fmt.Errorf("%w: unbalanced mapping object", ErrParse)
	}
	var mapping map[string]chatgptNode
	if err := json.Unmarshal([]byte(blob), &mapping); err != nil {
		return nil, fmt.Errorf("%w: mapping decode: %v", ErrParse, err)
	}

	// Mapping is a tree keyed by node id; create_time orders the messages
	// without needing to walk parent/children links.
	type timed struct {
		t    float64
		turn Turn
	}
	var msgs []timed
	for _, n := range mapping {
		if n.Message == nil || n.Message.Content.ContentType != "text" {
			continue
		}
		var parts []string
		for _, p := range n.Message.Content.Parts {
			if s, ok := p.(string); ok && s != "" {
				parts = append(parts, s)
			}
		}
		text := strings.Join(parts, "\n")
		if text == "" || n.Message.Author.Role == "system" {
			continue
		}
		msgs = append(msgs, timed{n.Message.CreateTime, Turn{Role: n.Message.Author.Role, Content: text}})
	}
	if len(msgs) == 0 {
		return nil, fmt.Errorf("%w: mapping has no text messages", ErrParse)
	}
	sort.SliceStable(msgs, func(i, j int) bool { return msgs[i].t < msgs[j].t })
	for _, m := range msgs {
		conv.Turns = append(conv.Turns, m.turn)
	}
	return conv, nil
}

// parseChatGPTFlight handles the React Router 7 framing, where the share
// payload streams through window.__reactRouterContext as turbo-stream v2
// chunks: each enqueue() argument is a JS string literal whose first line is
// one flat JSON array, with every value at an index and containers holding
// integer back-references. The key "mapping" is just an array element there
// (followed by a comma), so the legacy `"mapping":` anchors never match.
func parseChatGPTFlight(body string) (*Conversation, error) {
	const mark = `streamController.enqueue("`
	for rest := body; ; {
		i := strings.Index(rest, mark)
		if i < 0 {
			return nil, fmt.Errorf("%w: no conversation payload in stream chunks", ErrParse)
		}
		rest = rest[i+len(mark):]
		chunk, ok := decodeJSStringPrefix(rest)
		if !ok {
			continue
		}
		// The conversation is the first line of the chunk and is always a
		// flat array; lines like `P451:[{}]` resolve deferred promises and
		// object payloads belong to the legacy framing.
		line, _, _ := strings.Cut(chunk, "\n")
		if !strings.HasPrefix(line, "[") {
			continue
		}
		var arr []json.RawMessage
		if err := json.Unmarshal([]byte(line), &arr); err != nil || len(arr) == 0 {
			continue
		}
		f := &flightTable{arr: arr, memo: make(map[int]any, len(arr))}
		root, err := f.resolve(0, 0)
		if err != nil {
			continue
		}
		if conv, err := flightConversation(root); err == nil {
			return conv, nil
		}
	}
}

// decodeJSStringPrefix decodes the JS double-quoted string literal whose
// body starts at s (after the opening quote). All escapes observed in these
// pages are JSON-compatible, so the literal decodes as a JSON string.
func decodeJSStringPrefix(s string) (string, bool) {
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case '"':
			var out string
			if json.Unmarshal([]byte(`"`+s[:i]+`"`), &out) != nil {
				return "", false
			}
			return out, true
		}
	}
	return "", false
}

// flightTable hydrates a turbo-stream v2 flat value table. Index i holds an
// encoding: scalars are literal; objects map "_<keyIndex>" to a value index;
// plain arrays hold value indices; arrays whose first element is a string
// are tagged specials (promises, dates, …) that carry no transcript data.
// Negative "indices" are sentinels (-5 = null, etc.), never lookups.
type flightTable struct {
	arr  []json.RawMessage
	memo map[int]any
}

// flightMaxDepth bounds resolve's recursion. Real payloads nest a few dozen
// levels; without a bound, a linear back-reference chain (arr[i] = [i+1])
// recurses once per element and a deep enough chain is a fatal stack
// overflow (unrecoverable — recover() cannot catch it). The 8MB body cap in
// fetchPage happens to keep attacker chains below the fatal depth today, but
// that safety is incidental; this guard makes it an invariant. 10000 matches
// encoding/json's own nesting limit.
const flightMaxDepth = 10000

func (f *flightTable) resolve(i, depth int) (any, error) {
	if depth > flightMaxDepth {
		return nil, fmt.Errorf("reference chain deeper than %d", flightMaxDepth)
	}
	if i < 0 {
		return nil, nil
	}
	if i >= len(f.arr) {
		return nil, fmt.Errorf("reference %d out of range", i)
	}
	if v, ok := f.memo[i]; ok {
		return v, nil
	}
	raw := f.arr[i]
	switch raw[0] {
	case '{':
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			return nil, err
		}
		obj := make(map[string]any, len(fields))
		f.memo[i] = obj // before recursing: subtrees are shared
		for k, vraw := range fields {
			ki, err := strconv.Atoi(strings.TrimPrefix(k, "_"))
			if err != nil || !strings.HasPrefix(k, "_") {
				return nil, fmt.Errorf("bad object key %q", k)
			}
			kv, err := f.resolve(ki, depth+1)
			if err != nil {
				return nil, err
			}
			ks, ok := kv.(string)
			if !ok {
				return nil, fmt.Errorf("key reference %d is not a string", ki)
			}
			vi, err := strconv.Atoi(string(vraw))
			if err != nil {
				return nil, err
			}
			vv, err := f.resolve(vi, depth+1)
			if err != nil {
				return nil, err
			}
			obj[ks] = vv
		}
		return obj, nil
	case '[':
		var elems []json.RawMessage
		if err := json.Unmarshal(raw, &elems); err != nil {
			return nil, err
		}
		if len(elems) > 0 && elems[0][0] == '"' {
			f.memo[i] = nil
			return nil, nil
		}
		list := make([]any, len(elems))
		f.memo[i] = list
		for j, e := range elems {
			ei, err := strconv.Atoi(string(e))
			if err != nil {
				return nil, err
			}
			ev, err := f.resolve(ei, depth+1)
			if err != nil {
				return nil, err
			}
			list[j] = ev
		}
		return list, nil
	default:
		var v any
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil, err
		}
		f.memo[i] = v
		return v, nil
	}
}

// flightConversation extracts the share payload from a hydrated route tree:
// root.loaderData.<route>.serverResponse.data, which has the same inner
// shape as the legacy framing plus linear_conversation, the nodes already
// ordered root→leaf. create_time is nullable and unordered in this framing
// (an assistant's final text can predate its own reasoning), so array order
// is authoritative.
func flightConversation(root any) (*Conversation, error) {
	rm, _ := root.(map[string]any)
	ld, _ := rm["loaderData"].(map[string]any)
	for _, route := range ld {
		r, _ := route.(map[string]any)
		sr, _ := r["serverResponse"].(map[string]any)
		data, ok := sr["data"].(map[string]any)
		if !ok {
			continue
		}
		conv := &Conversation{Provider: "openai"}
		for _, key := range []string{"default_model_slug", "model_slug"} {
			if s, ok := data[key].(string); ok && conv.Model == "" {
				conv.Model = s
			}
		}
		nodes, _ := data["linear_conversation"].([]any)
		if nodes == nil {
			nodes = flightMappingOrder(data["mapping"])
		}
		for _, n := range nodes {
			node, _ := n.(map[string]any)
			if turn, ok := flightTurn(node); ok {
				conv.Turns = append(conv.Turns, turn)
			}
		}
		if len(conv.Turns) == 0 {
			return nil, fmt.Errorf("%w: conversation has no text messages", ErrParse)
		}
		return conv, nil
	}
	return nil, fmt.Errorf("%w: no share data under loaderData", ErrParse)
}

// flightMappingOrder linearizes the mapping tree root→leaf by children
// links, for payloads without linear_conversation.
func flightMappingOrder(v any) []any {
	mapping, _ := v.(map[string]any)
	var order []any
	seen := make(map[string]bool)
	var visit func(id string)
	visit = func(id string) {
		if seen[id] {
			return
		}
		seen[id] = true
		node, ok := mapping[id].(map[string]any)
		if !ok {
			return
		}
		order = append(order, node)
		children, _ := node["children"].([]any)
		for _, c := range children {
			if s, ok := c.(string); ok {
				visit(s)
			}
		}
	}
	for id, n := range mapping {
		if node, ok := n.(map[string]any); ok {
			if p, ok := node["parent"].(string); !ok || p == "" {
				visit(id)
			}
		}
	}
	return order
}

// flightTurn filters one node to a visible user/assistant text turn. The
// only real user turn on current pages can be multimodal_text (image part +
// string part), and hidden machinery turns (custom-instruction stubs,
// thoughts, recaps) share the user/assistant roles, so role alone is not
// enough.
func flightTurn(node map[string]any) (Turn, bool) {
	msg, _ := node["message"].(map[string]any)
	author, _ := msg["author"].(map[string]any)
	role, _ := author["role"].(string)
	if role != "user" && role != "assistant" {
		return Turn{}, false
	}
	meta, _ := msg["metadata"].(map[string]any)
	if hidden, _ := meta["is_visually_hidden_from_conversation"].(bool); hidden {
		return Turn{}, false
	}
	content, _ := msg["content"].(map[string]any)
	ct, _ := content["content_type"].(string)
	if ct != "text" && ct != "multimodal_text" {
		return Turn{}, false
	}
	parts, _ := content["parts"].([]any)
	var texts []string
	for _, p := range parts {
		if s, ok := p.(string); ok && s != "" {
			texts = append(texts, s)
		}
	}
	text := strings.Join(texts, "\n")
	if text == "" {
		return Turn{}, false
	}
	return Turn{Role: role, Content: text}, true
}
