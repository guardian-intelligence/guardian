package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// claudeSource extracts transcripts from Claude share pages
// (claude.ai/share/<uuid>). As with ChatGPT, extraction targets the stable
// inner shape — the "chat_messages" array — not the framework wrapper.
type claudeSource struct{}

var claudePath = regexp.MustCompile(`^/share/[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$`)

func (claudeSource) Match(u *url.URL) bool {
	return u.Host == "claude.ai" && claudePath.MatchString(strings.ToLower(u.Path))
}

func (claudeSource) Fetch(ctx context.Context, u *url.URL) (*Conversation, error) {
	body, err := fetchPage(ctx, sourceClaude, u)
	if err != nil {
		return nil, err // transport reasons already counted in fetchPage
	}
	conv, err := parseClaude(body)
	if err != nil {
		// No no_conversation split here: Claude's soft-404 signature is
		// unknown until observed live, so every extraction miss counts as
		// parse. The ok side must still count, or the day this source ships
		// its successes vanish from UpstreamFetchDegraded's denominator and
		// overstate the failure share.
		fetchTotal.WithLabelValues(sourceClaude, "parse").Inc()
		return nil, err
	}
	fetchTotal.WithLabelValues(sourceClaude, "ok").Inc()
	return conv, nil
}

type claudeMessage struct {
	Sender  string `json:"sender"`
	Text    string `json:"text"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

func parseClaude(body string) (*Conversation, error) {
	if !strings.Contains(body, `"chat_messages":`) && strings.Contains(body, `\"chat_messages\":`) {
		body = strings.ReplaceAll(body, `\"`, `"`)
		body = strings.ReplaceAll(body, `\\`, `\`)
	}

	conv := &Conversation{Provider: "anthropic"}
	if m := regexp.MustCompile(`"model":\s*"([^"]+)"`).FindStringSubmatch(body); m != nil {
		conv.Model = m[1]
	}

	idx := strings.Index(body, `"chat_messages":`)
	if idx < 0 {
		return nil, fmt.Errorf("%w: no chat_messages array", ErrParse)
	}
	start := idx + len(`"chat_messages":`)
	for start < len(body) && (body[start] == ' ' || body[start] == '\n') {
		start++
	}
	blob, ok := extractJSON(body, start)
	if !ok {
		return nil, fmt.Errorf("%w: unbalanced chat_messages array", ErrParse)
	}
	var msgs []claudeMessage
	if err := json.Unmarshal([]byte(blob), &msgs); err != nil {
		return nil, fmt.Errorf("%w: chat_messages decode: %v", ErrParse, err)
	}

	for _, m := range msgs {
		text := m.Text
		if text == "" {
			var parts []string
			for _, c := range m.Content {
				if c.Type == "text" && c.Text != "" {
					parts = append(parts, c.Text)
				}
			}
			text = strings.Join(parts, "\n")
		}
		if text == "" {
			continue
		}
		role := m.Sender
		if role == "human" {
			role = "user"
		}
		conv.Turns = append(conv.Turns, Turn{Role: role, Content: text})
	}
	if len(conv.Turns) == 0 {
		return nil, fmt.Errorf("%w: chat_messages has no text", ErrParse)
	}
	return conv, nil
}
