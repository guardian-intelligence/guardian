package chunkie

import (
	"strings"
	"testing"
)

func TestParseVocabConsequential(t *testing.T) {
	v, err := parseVocab(strings.NewReader(`
# a game with one durable fact
depart 2
action 5 purchase
consequential 5
`))
	if err != nil {
		t.Fatal(err)
	}
	if !v.Consequential[5] || len(v.Consequential) != 1 {
		t.Fatalf("Consequential = %v, want {5}", v.Consequential)
	}
	if _, err := parseVocab(strings.NewReader("consequential 5 extra")); err == nil {
		t.Fatal("malformed consequential directive must refuse")
	}
	if _, err := parseVocab(strings.NewReader("consequential 70000")); err == nil {
		t.Fatal("kind outside u16 must refuse")
	}
	bare, err := loadVocab("")
	if err != nil {
		t.Fatal(err)
	}
	if bare.Consequential == nil {
		t.Fatal("bare vocabulary must carry an empty Consequential table, not nil")
	}
}
