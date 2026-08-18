package main

import "regexp"

// Props is the one freeform client-supplied blob in the events schema, and
// path/referrer can carry OAuth material in query strings. Credential shapes
// are masked before rows are batched; the pattern set mirrors the OTel
// collector's redaction processor so both sinks enforce the same contract.
var credentialShapePatterns = []*regexp.Regexp{
	regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{20,255}`),
	regexp.MustCompile(`github_pat_[A-Za-z0-9_]{20,255}`),
	regexp.MustCompile(`[Bb]earer\s+[A-Za-z0-9._~+/=-]{16,}`),
	regexp.MustCompile(`eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9._-]{10,}`),
	regexp.MustCompile(`(?i)(password|passwd|secret|api[_-]?key|access[_-]?token|refresh[_-]?token|client[_-]?secret|totp[_-]?(seed|secret))\s*[=:]\s*[^\s&"']{6,}`),
}

var queryCredentialPattern = regexp.MustCompile(`(?i)([?&](?:code|state|access_token|id_token)=)[^\s&"']{6,}`)

const redactedMark = "[REDACTED]"

func redactCredentialShapes(s string) string {
	if s == "" {
		return s
	}
	out := queryCredentialPattern.ReplaceAllString(s, "${1}"+redactedMark)
	for _, pattern := range credentialShapePatterns {
		out = pattern.ReplaceAllString(out, redactedMark)
	}
	return out
}
