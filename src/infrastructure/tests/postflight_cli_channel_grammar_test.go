package tests

// The CLI release train has no channel field anywhere: a release's channel is
// its tag's shape, and three independent programs decide it. The cutter picks
// the tag it mints and the listing it dedups against, `dist/install.sh`
// resolves `--channel` over the public listing, and the install canary picks
// each channel's newest release to assert the pinned digest against. Nothing
// but this test holds them to one grammar, and the failure it prevents is
// quiet: a consumer that resolves the wrong release still reports success,
// against the wrong bytes.

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
)

const (
	releaseWorkflowFile = ".github/workflows/postflight-cli-release.yml"
	installerFile       = "src/postflight/cli/dist/install.sh"
	installCanaryFile   = "src/infrastructure/deployments/guardian/promotion/cli-install-canary.yaml"
)

// Every tag shape the listing can hold, and the one channel each belongs to.
// `postflight-cli/v0.2.0-rc.1` is the one candidate published before the `rc-`
// prefix existed; registry history is permanent, so every consumer has to keep
// reading it as a candidate. `v0.4.0-rc.9` carries `prerelease: false` — the
// flag is mutable in the GitHub UI, so shape and not the flag is what keeps a
// candidate out of the stable channel.
var channelGrammarSamples = []struct {
	tag        string
	prerelease bool
	channel    string
}{
	{"postflight-cli/nightly-20260726", true, "nightly"},
	{"postflight-cli/nightly-20260726-2134", true, "nightly"},
	{"postflight-cli/rc-20260726", true, "rc"},
	{"postflight-cli/rc-20260726-2134", true, "rc"},
	{"postflight-cli/v0.2.0-rc.1", true, "rc"},
	{"postflight-cli/v0.4.0-rc.9", false, "rc"},
	{"postflight-cli/v0.3.0", false, "stable"},
	{"postflight-cli/v1.0.0", false, "stable"},
	{"dataset-2026-07-20", false, ""},
}

var releaseChannels = []string{"nightly", "rc", "stable"}

func TestPostflightCliChannelGrammarMovesTogether(t *testing.T) {
	cutter := cutterChannelMatchers(t)
	installer := installerChannelMatchers(t)
	canary := canaryChannelMatchers(t)

	consumers := map[string]func(channel, tag string, prerelease bool) bool{
		releaseWorkflowFile: cutter.matches,
		installerFile:       installer.matches,
		installCanaryFile:   canary.matches,
	}

	for _, sample := range channelGrammarSamples {
		for _, channel := range releaseChannels {
			want := channel == sample.channel
			for file, matches := range consumers {
				if got := matches(channel, sample.tag, sample.prerelease); got != want {
					t.Errorf("%s resolves %q (prerelease=%v) on channel %s as %v, want %v — the release channels are the tag grammar, and this consumer no longer reads it the way the others do",
						file, sample.tag, sample.prerelease, channel, got, want)
				}
			}
		}
	}

	// What the cutter mints has to be what the other two resolve. The tag
	// templates are read out of the workflow rather than restated here, so a
	// renamed prefix fails against the matchers instead of against a copy.
	for channel, minted := range cutterMintedTags(t) {
		for _, tag := range minted.tags {
			for file, matches := range consumers {
				if !matches(channel, tag, minted.prerelease) {
					t.Errorf("%s cuts %q on channel %s, which %s does not resolve as %s",
						releaseWorkflowFile, tag, channel, file, channel)
				}
			}
		}
	}
}

// --- the cutter: jq filters over the release listing ---

type cutterMatchers struct {
	filters map[string]jqBool
}

func (c cutterMatchers) matches(channel, tag string, _ bool) bool {
	filter, ok := c.filters[channel]
	if !ok {
		return false
	}
	return filter(tag)
}

func cutterChannelMatchers(t *testing.T) cutterMatchers {
	t.Helper()

	workflow := readText(t, runfilePath(releaseWorkflowFile))
	arm := regexp.MustCompile(`(?m)^\s*(nightly|rc|stable)\) match='([^']*)' ;;$`)
	filters := map[string]jqBool{}
	for _, match := range arm.FindAllStringSubmatch(workflow, -1) {
		filter, err := parseJQBool(match[2])
		if err != nil {
			t.Fatalf("%s: channel %s selects releases with %q, which this test cannot evaluate: %v", releaseWorkflowFile, match[1], match[2], err)
		}
		filters[match[1]] = filter
	}
	assertCoversEveryChannel(t, releaseWorkflowFile+" releases()", filters)
	return cutterMatchers{filters: filters}
}

type mintedTags struct {
	tags       []string
	prerelease bool
}

// The cut step composes each channel's tag from shell variables; substituting
// the ones it can hold turns the template into a tag the matchers can be asked
// about. A variable with no substitution here is a grammar change nobody has
// taught this test about, so it fails rather than guesses.
func cutterMintedTags(t *testing.T) map[string]mintedTags {
	t.Helper()

	substitutions := map[string]string{"$day": "20260726", "$version": "0.3.0"}
	workflow := readText(t, runfilePath(releaseWorkflowFile))

	minted := map[string]mintedTags{}
	channel := ""
	armStart := regexp.MustCompile(`^\s*(nightly|rc|stable)\)\s*$`)
	tagLine := regexp.MustCompile(`^\s*tag="([^"]*)"\s*$`)
	flagsLine := regexp.MustCompile(`^\s*flags=\((--prerelease|--latest)\)\s*$`)
	for _, line := range strings.Split(workflow, "\n") {
		if match := armStart.FindStringSubmatch(line); match != nil {
			channel = match[1]
			continue
		}
		if channel == "" {
			continue
		}
		if match := tagLine.FindStringSubmatch(line); match != nil {
			tag := match[1]
			for variable, value := range substitutions {
				tag = strings.ReplaceAll(tag, variable, value)
			}
			if strings.Contains(tag, "$") {
				t.Fatalf("%s: channel %s composes its tag as %q, which holds a variable this test cannot fill in", releaseWorkflowFile, channel, match[1])
			}
			entry := minted[channel]
			entry.tags = append(entry.tags, tag)
			minted[channel] = entry
			continue
		}
		if match := flagsLine.FindStringSubmatch(line); match != nil {
			entry := minted[channel]
			entry.prerelease = match[1] == "--prerelease"
			minted[channel] = entry
		}
	}

	// A same-day second cut takes an -HHMM suffix on every channel that allows
	// one, and that longer tag has to resolve to the same channel.
	for channel, entry := range minted {
		if len(entry.tags) == 0 {
			t.Fatalf("%s: channel %s composes no tag", releaseWorkflowFile, channel)
		}
		if channel == "stable" {
			continue
		}
		entry.tags = append(entry.tags, entry.tags[0]+"-2134")
		minted[channel] = entry
	}

	assertCoversEveryChannel(t, releaseWorkflowFile+" tag composition", minted)
	return minted
}

// --- the installer: shell case patterns over the tag and the prerelease flag ---

type installerArm struct {
	pattern      *regexp.Regexp
	accepts      bool
	needsRelease bool
}

type installerMatchers struct {
	arms map[string][]installerArm
}

func (i installerMatchers) matches(channel, tag string, prerelease bool) bool {
	for _, arm := range i.arms[channel] {
		if !arm.pattern.MatchString(tag) {
			continue
		}
		// `case` stops at the first pattern that matches, whatever it does.
		return arm.accepts && !(arm.needsRelease && prerelease)
	}
	return false
}

func installerChannelMatchers(t *testing.T) installerMatchers {
	t.Helper()

	installer := readText(t, runfilePath(installerFile))
	start := strings.Index(installer, "matches_channel() {")
	if start < 0 {
		t.Fatalf("%s declares no matches_channel(); the installer's channel grammar moved", installerFile)
	}
	body := installer[start:]
	if end := strings.Index(body, "\n}\n"); end >= 0 {
		body = body[:end]
	}

	arms := map[string][]installerArm{}
	channel := ""
	armStart := regexp.MustCompile(`^\s*(nightly|rc|stable)\)\s*$`)
	patternLine := regexp.MustCompile(`^\s*("[^)]*)\)\s*(.*?)\s*;;\s*$`)
	for _, line := range strings.Split(body, "\n") {
		if match := armStart.FindStringSubmatch(line); match != nil {
			channel = match[1]
			continue
		}
		match := patternLine.FindStringSubmatch(line)
		if match == nil || channel == "" {
			continue
		}
		arm := installerArm{pattern: shellGlobPattern(t, match[1])}
		switch action := match[2]; {
		case action == "":
			// Matched and did nothing: `case` is done, so the tag is refused.
		case action == "return 0":
			arm.accepts = true
		case action == `[ "$3" = "false" ] && return 0`:
			arm.accepts, arm.needsRelease = true, true
		default:
			t.Fatalf("%s: channel %s answers pattern %s with %q, which this test cannot model", installerFile, channel, match[1], action)
		}
		arms[channel] = append(arms[channel], arm)
	}
	assertCoversEveryChannel(t, installerFile+" matches_channel()", arms)
	return installerMatchers{arms: arms}
}

// A shell case pattern of quoted literals and `*` wildcards, with $TAG_PREFIX
// expanded to the value the installer holds it at.
func shellGlobPattern(t *testing.T, pattern string) *regexp.Regexp {
	t.Helper()

	var expression strings.Builder
	expression.WriteString("^")
	quoted := false
	for _, char := range pattern {
		switch {
		case char == '"':
			quoted = !quoted
		case char == '*' && !quoted:
			expression.WriteString(".*")
		default:
			expression.WriteString(regexp.QuoteMeta(string(char)))
		}
	}
	expression.WriteString("$")
	compiled, err := regexp.Compile(strings.ReplaceAll(expression.String(), regexp.QuoteMeta("$TAG_PREFIX"), "postflight-cli"))
	if err != nil {
		t.Fatalf("%s: case pattern %s does not translate to a regular expression: %v", installerFile, pattern, err)
	}
	return compiled
}

// --- the canary: jq test() regexes with an optional exclusion ---

type canaryArm struct {
	include *regexp.Regexp
	exclude *regexp.Regexp
}

type canaryMatchers struct {
	arms map[string]canaryArm
}

func (c canaryMatchers) matches(channel, tag string, _ bool) bool {
	arm, ok := c.arms[channel]
	if !ok {
		return false
	}
	return arm.include.MatchString(tag) && (arm.exclude == nil || !arm.exclude.MatchString(tag))
}

func canaryChannelMatchers(t *testing.T) canaryMatchers {
	t.Helper()

	canary := readText(t, runfilePath(installCanaryFile))
	arm := regexp.MustCompile(`(?m)^\s*(nightly|rc|stable)\) include='([^']*)'; exclude='([^']*)' ;;$`)
	arms := map[string]canaryArm{}
	for _, match := range arm.FindAllStringSubmatch(canary, -1) {
		parsed := canaryArm{include: mustCompileJQRegex(t, installCanaryFile, match[2])}
		if match[3] != "" {
			parsed.exclude = mustCompileJQRegex(t, installCanaryFile, match[3])
		}
		arms[match[1]] = parsed
	}
	assertCoversEveryChannel(t, installCanaryFile+" channel grammar", arms)
	return canaryMatchers{arms: arms}
}

func mustCompileJQRegex(t *testing.T, file, expression string) *regexp.Regexp {
	t.Helper()

	compiled, err := regexp.Compile(expression)
	if err != nil {
		t.Fatalf("%s: %q is not a regular expression: %v", file, expression, err)
	}
	return compiled
}

func assertCoversEveryChannel[V any](t *testing.T, context string, byChannel map[string]V) {
	t.Helper()

	for _, channel := range releaseChannels {
		if _, ok := byChannel[channel]; !ok {
			t.Fatalf("%s declares no grammar for channel %s", context, channel)
		}
	}
	if len(byChannel) != len(releaseChannels) {
		t.Fatalf("%s declares grammar for channels outside %v", context, releaseChannels)
	}
}

// --- the jq subset the cutter's listing filters are written in ---

type jqBool func(tag string) bool

type jqParser struct {
	tokens []string
	at     int
}

var jqToken = regexp.MustCompile(`startswith\("[^"]*"\)|contains\("[^"]*"\)|\(|\)|\||not|and|or`)

func parseJQBool(expression string) (jqBool, error) {
	parser := &jqParser{tokens: jqToken.FindAllString(expression, -1)}
	if strings.Join(parser.tokens, "") != strings.Join(strings.Fields(expression), "") {
		return nil, fmt.Errorf("holds something other than startswith/contains/not/and/or")
	}
	parsed, err := parser.parseOr()
	if err != nil {
		return nil, err
	}
	if parser.at != len(parser.tokens) {
		return nil, fmt.Errorf("has trailing %q", strings.Join(parser.tokens[parser.at:], " "))
	}
	return parsed, nil
}

func (p *jqParser) parseOr() (jqBool, error) {
	left, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	for p.peek() == "or" {
		p.at++
		right, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		first, second := left, right
		left = func(tag string) bool { return first(tag) || second(tag) }
	}
	return left, nil
}

func (p *jqParser) parseAnd() (jqBool, error) {
	left, err := p.parsePipe()
	if err != nil {
		return nil, err
	}
	for p.peek() == "and" {
		p.at++
		right, err := p.parsePipe()
		if err != nil {
			return nil, err
		}
		first, second := left, right
		left = func(tag string) bool { return first(tag) && second(tag) }
	}
	return left, nil
}

func (p *jqParser) parsePipe() (jqBool, error) {
	term, err := p.parseTerm()
	if err != nil {
		return nil, err
	}
	for p.peek() == "|" {
		p.at++
		if p.peek() != "not" {
			return nil, fmt.Errorf("pipes into %q, and `not` is the only filter this test evaluates", p.peek())
		}
		p.at++
		inner := term
		term = func(tag string) bool { return !inner(tag) }
	}
	return term, nil
}

func (p *jqParser) parseTerm() (jqBool, error) {
	token := p.peek()
	switch {
	case token == "(":
		p.at++
		inner, err := p.parseOr()
		if err != nil {
			return nil, err
		}
		if p.peek() != ")" {
			return nil, fmt.Errorf("has an unclosed group")
		}
		p.at++
		return inner, nil
	case strings.HasPrefix(token, "startswith("):
		p.at++
		literal := token[len(`startswith("`) : len(token)-2]
		return func(tag string) bool { return strings.HasPrefix(tag, literal) }, nil
	case strings.HasPrefix(token, "contains("):
		p.at++
		literal := token[len(`contains("`) : len(token)-2]
		return func(tag string) bool { return strings.Contains(tag, literal) }, nil
	}
	return nil, fmt.Errorf("expects a term, found %q", token)
}

func (p *jqParser) peek() string {
	if p.at >= len(p.tokens) {
		return ""
	}
	return p.tokens[p.at]
}
