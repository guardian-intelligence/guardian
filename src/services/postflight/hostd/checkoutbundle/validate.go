package checkoutbundle

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

var (
	errInvalid   = errors.New("checkout request is invalid")
	errForbidden = errors.New("checkout request is not permitted for this assignment")

	shaPattern = regexp.MustCompile(`\A[0-9a-f]{40}\z`)
)

// bundleRequest is the decoded, unvalidated request body.
type bundleRequest struct {
	Repository  string `json:"repository"`
	Ref         string `json:"ref"`
	SHA         string `json:"sha"`
	Have        string `json:"have,omitempty"`
	GitHubToken string `json:"github_token"`
}

// checkoutSpec is the validated request.
type checkoutSpec struct {
	Repository  string
	Ref         string
	SHA         string
	Have        string
	GitHubToken string
}

// validateRequest normalizes the body and enforces the assignment boundary.
func validateRequest(req bundleRequest, identity AssignmentIdentity) (checkoutSpec, error) {
	repository, err := normalizeRepository(req.Repository)
	if err != nil {
		return checkoutSpec{}, err
	}
	if !strings.EqualFold(repository, strings.TrimSpace(identity.RepositoryFullName)) {
		return checkoutSpec{}, fmt.Errorf("%w: repository does not match the assignment", errForbidden)
	}
	sha, err := normalizeSHA(req.SHA)
	if err != nil {
		return checkoutSpec{}, err
	}
	have := ""
	if strings.TrimSpace(req.Have) != "" {
		have, err = normalizeSHA(req.Have)
		if err != nil {
			return checkoutSpec{}, err
		}
	}
	ref, err := normalizeRef(req.Ref)
	if err != nil {
		return checkoutSpec{}, err
	}
	token, err := normalizeGitHubToken(req.GitHubToken)
	if err != nil {
		return checkoutSpec{}, err
	}
	return checkoutSpec{Repository: repository, Ref: ref, SHA: sha, Have: have, GitHubToken: token}, nil
}

func normalizeRepository(repository string) (string, error) {
	repository = strings.TrimSpace(repository)
	owner, name, ok := strings.Cut(repository, "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return "", fmt.Errorf("%w: repository must be owner/name", errInvalid)
	}
	for _, part := range []string{owner, name} {
		if strings.ContainsAny(part, "\x00\r\n\t ") || strings.Contains(part, "..") {
			return "", fmt.Errorf("%w: invalid repository", errInvalid)
		}
	}
	return repository, nil
}

func normalizeSHA(sha string) (string, error) {
	sha = strings.ToLower(strings.TrimSpace(sha))
	if !shaPattern.MatchString(sha) {
		return "", fmt.Errorf("%w: sha must be a 40-character commit sha", errInvalid)
	}
	return sha, nil
}

// normalizeRef accepts an empty ref (the SHA-only fallback still works) or a
// full GitHub ref. refs/pull/N/{head,merge} is how pull-request jobs arrive.
func normalizeRef(ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", nil
	}
	if len(ref) > 1024 || strings.ContainsAny(ref, "\x00\r\n\t ") || strings.Contains(ref, "..") {
		return "", fmt.Errorf("%w: invalid ref", errInvalid)
	}
	switch {
	case strings.HasPrefix(ref, "refs/heads/"),
		strings.HasPrefix(ref, "refs/tags/"),
		strings.HasPrefix(ref, "refs/pull/"):
		return ref, nil
	default:
		return "", fmt.Errorf("%w: ref must be a full GitHub ref", errInvalid)
	}
}

func normalizeGitHubToken(token string) (string, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return "", fmt.Errorf("%w: github token is required", errInvalid)
	}
	if strings.ContainsAny(token, "\x00\r\n") {
		return "", fmt.Errorf("%w: github token contains control characters", errInvalid)
	}
	return token, nil
}
