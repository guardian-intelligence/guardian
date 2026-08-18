package main

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"regexp"
)

var (
	typePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)
	idPattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,255}$`)
)

var permissionContract = map[string]map[string]struct{}{
	"organization": set(
		"manage_members",
		"manage_billing",
		"manage_integrations",
		"configure_repository",
		"run_job",
		"view",
	),
	"github_installation": set("view", "manage"),
	"postflight_project":  set("view", "manage", "run_job"),
	"postflight_repository": set(
		"view",
		"manage",
		"configure",
		"run_job",
	),
}

var relationContract = map[string]map[string]string{
	"organization": {
		"owner":  "guardian_account",
		"admin":  "guardian_account",
		"member": "guardian_account",
	},
	"github_installation": {
		"organization": "organization",
	},
	"postflight_project": {
		"organization": "organization",
	},
	"postflight_repository": {
		"project":      "postflight_project",
		"installation": "github_installation",
		"admin":        "guardian_account",
	},
}

func set(values ...string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}

func validateObject(object objectRef) error {
	if !typePattern.MatchString(object.Type) {
		return errors.New("object type is invalid")
	}
	if !idPattern.MatchString(object.ID) {
		return errors.New("object id is invalid")
	}
	return nil
}

func validateSubject(subject subjectRef) error {
	if err := validateObject(subject.Object); err != nil {
		return err
	}
	if subject.OptionalRelation != "" && !typePattern.MatchString(subject.OptionalRelation) {
		return errors.New("optional subject relation is invalid")
	}
	return nil
}

func allowedPermission(objectType, permission string) bool {
	_, ok := permissionContract[objectType][permission]
	return ok
}

func allowedRelation(resourceType, relation, subjectType string) bool {
	expected, ok := relationContract[resourceType][relation]
	return ok && expected == subjectType
}

func tlsConfig(roots *x509.CertPool) *tls.Config {
	return &tls.Config{
		MinVersion: tls.VersionTLS13,
		RootCAs:    roots,
	}
}
