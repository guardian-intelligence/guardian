package checkoutbundle

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateRequest(t *testing.T) {
	identity := AssignmentIdentity{RepositoryFullName: "acme/widget"}
	valid := bundleRequest{
		Repository:  "acme/widget",
		Ref:         "refs/heads/main",
		SHA:         strings.Repeat("ab", 20),
		Have:        strings.Repeat("cd", 20),
		GitHubToken: "ghs_dummy",
	}

	t.Run("valid", func(t *testing.T) {
		spec, err := validateRequest(valid, identity)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if spec.Repository != "acme/widget" || spec.Ref != "refs/heads/main" || spec.Have != valid.Have {
			t.Fatalf("unexpected spec: %+v", spec)
		}
	})

	t.Run("sha is lowercased", func(t *testing.T) {
		req := valid
		req.SHA = strings.ToUpper(req.SHA)
		spec, err := validateRequest(req, identity)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if spec.SHA != strings.ToLower(req.SHA) {
			t.Fatalf("sha not normalized: %s", spec.SHA)
		}
	})

	t.Run("repository casing still matches assignment", func(t *testing.T) {
		req := valid
		req.Repository = "ACME/Widget"
		if _, err := validateRequest(req, identity); err != nil {
			t.Fatalf("case-insensitive assignment match failed: %v", err)
		}
	})

	t.Run("empty ref allowed", func(t *testing.T) {
		req := valid
		req.Ref = ""
		if _, err := validateRequest(req, identity); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("pull request refs allowed", func(t *testing.T) {
		for _, ref := range []string{"refs/pull/7/merge", "refs/pull/7/head", "refs/tags/v1.0.0"} {
			req := valid
			req.Ref = ref
			if _, err := validateRequest(req, identity); err != nil {
				t.Fatalf("ref %s rejected: %v", ref, err)
			}
		}
	})

	invalidCases := map[string]bundleRequest{
		"missing repository":       {Ref: valid.Ref, SHA: valid.SHA, GitHubToken: valid.GitHubToken},
		"repository without owner": {Repository: "/widget", Ref: valid.Ref, SHA: valid.SHA, GitHubToken: valid.GitHubToken},
		"repository with dotdot":   {Repository: "acme/..", Ref: valid.Ref, SHA: valid.SHA, GitHubToken: valid.GitHubToken},
		"repository with space":    {Repository: "acme/wid get", Ref: valid.Ref, SHA: valid.SHA, GitHubToken: valid.GitHubToken},
		"short sha":                {Repository: valid.Repository, Ref: valid.Ref, SHA: "abc123", GitHubToken: valid.GitHubToken},
		"non-hex sha":              {Repository: valid.Repository, Ref: valid.Ref, SHA: strings.Repeat("zz", 20), GitHubToken: valid.GitHubToken},
		"short have":               {Repository: valid.Repository, Ref: valid.Ref, SHA: valid.SHA, Have: "abc123", GitHubToken: valid.GitHubToken},
		"non-hex have":             {Repository: valid.Repository, Ref: valid.Ref, SHA: valid.SHA, Have: strings.Repeat("zz", 20), GitHubToken: valid.GitHubToken},
		"partial ref":              {Repository: valid.Repository, Ref: "main", SHA: valid.SHA, GitHubToken: valid.GitHubToken},
		"ref with dotdot":          {Repository: valid.Repository, Ref: "refs/heads/a..b", SHA: valid.SHA, GitHubToken: valid.GitHubToken},
		"ref with space":           {Repository: valid.Repository, Ref: "refs/heads/a b", SHA: valid.SHA, GitHubToken: valid.GitHubToken},
		"missing token":            {Repository: valid.Repository, Ref: valid.Ref, SHA: valid.SHA},
		"token with newline":       {Repository: valid.Repository, Ref: valid.Ref, SHA: valid.SHA, GitHubToken: "bad\ntoken"},
	}
	for name, req := range invalidCases {
		t.Run(name, func(t *testing.T) {
			_, err := validateRequest(req, identity)
			if !errors.Is(err, errInvalid) {
				t.Fatalf("expected errInvalid, got %v", err)
			}
		})
	}

	t.Run("repository not covered by assignment", func(t *testing.T) {
		req := valid
		req.Repository = "acme/other"
		_, err := validateRequest(req, identity)
		if !errors.Is(err, errForbidden) {
			t.Fatalf("expected errForbidden, got %v", err)
		}
	})
}
