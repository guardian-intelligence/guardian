package tests

// Electric read-path budget conformance. Raise these ceilings only via a
// reviewed PR with recorded bake-off rationale; connection allocation must
// stay aligned with the Connection budget table in each products
// postgres.yaml header.

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"
)

const (
	maxElectricShapeRootTables = 6
	maxElectricDBPoolSize      = 20
)

var createTableStatement = regexp.MustCompile(`(?i)\bCREATE\s+TABLE\b`)

func TestElectricShapeRootTableBudget(t *testing.T) {
	for _, stageDir := range productsStageDirs(t) {
		path := filepath.Join(stageDir, "ddl-configmap.yaml")
		cm := singleYAMLDoc(t, path)
		data := mapValue(cm["data"])
		if len(data) == 0 {
			t.Fatalf("%s has no data entries", path)
		}

		count := 0
		for _, value := range data {
			count += len(createTableStatement.FindAllString(stringValue(value), -1))
		}
		if count > maxElectricShapeRootTables {
			t.Fatalf("%s defines %d CREATE TABLE statements, want <= %d", path, count, maxElectricShapeRootTables)
		}
	}
}

func TestElectricDBPoolBudget(t *testing.T) {
	for _, stageDir := range productsStageDirs(t) {
		path := filepath.Join(stageDir, "electric.yaml")
		deployment := findDoc(t, yamlDocs(t, path), "Deployment", "electric")
		container := firstNamedContainer(t, deployment, "electric")

		value := envValue(container, "ELECTRIC_DB_POOL_SIZE")
		if value == "" {
			t.Fatalf("%s Deployment/electric lacks ELECTRIC_DB_POOL_SIZE", path)
		}
		poolSize, err := strconv.Atoi(value)
		if err != nil {
			t.Fatalf("%s ELECTRIC_DB_POOL_SIZE = %q, want integer <= %d", path, value, maxElectricDBPoolSize)
		}
		if poolSize > maxElectricDBPoolSize {
			t.Fatalf("%s ELECTRIC_DB_POOL_SIZE = %d, want <= %d", path, poolSize, maxElectricDBPoolSize)
		}
	}
}

func productsStageDirs(t *testing.T) []string {
	t.Helper()

	root := filepath.Join(repoRootFromRunfiles(t), "src/infrastructure/deployments/products")
	matches, err := filepath.Glob(filepath.Join(root, "*"))
	if err != nil {
		t.Fatalf("glob products stage dirs: %v", err)
	}
	if len(matches) == 0 {
		t.Fatalf("no products stage directories under %s", root)
	}

	var stageDirs []string
	for _, match := range matches {
		info, err := os.Stat(match)
		if err != nil {
			t.Fatalf("stat %s: %v", match, err)
		}
		if !info.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(match, "kustomization.yaml")); err != nil {
			t.Fatalf("stat products stage kustomization under %s: %v", match, err)
		}
		stageDirs = append(stageDirs, match)
	}
	if len(stageDirs) == 0 {
		t.Fatalf("no products stage kustomizations under %s", root)
	}
	return stageDirs
}

func firstNamedContainer(t *testing.T, doc map[string]interface{}, name string) map[string]interface{} {
	t.Helper()

	containers := sliceValue(nestedValue(t, doc, "spec", "template", "spec", "containers"))
	for _, item := range containers {
		container := mapValue(item)
		if stringValue(container["name"]) == name {
			return container
		}
	}
	t.Fatalf("%s/%s has no container named %q", stringValue(doc["kind"]), stringValue(mapValue(doc["metadata"])["name"]), name)
	return nil
}

func envValue(container map[string]interface{}, name string) string {
	for _, item := range sliceValue(container["env"]) {
		env := mapValue(item)
		if stringValue(env["name"]) == name {
			return stringValue(env["value"])
		}
	}
	return ""
}
