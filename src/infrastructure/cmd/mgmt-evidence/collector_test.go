package main

import (
	"encoding/json"
	"testing"
)

func TestParseObjectsAcceptsListAndSingleObject(t *testing.T) {
	list, err := parseObjects([]byte(`{"items":[{"kind":"Node","metadata":{"name":"n1"}},{"kind":"Node","metadata":{"name":"n2"}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("list length = %d, want 2", len(list))
	}

	single, err := parseObjects([]byte(`{"kind":"OpenBao","metadata":{"name":"guardian"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(single) != 1 || kindOf(single[0]) != "OpenBao" || nameOf(single[0]) != "guardian" {
		t.Fatalf("single object parse = %#v", single)
	}
}

func TestValidateCompanySiteRequiresReadyDeploymentAndIngressHost(t *testing.T) {
	objects := []object{
		mustObject(t, `{
		  "kind":"Deployment",
		  "metadata":{"name":"company-site","namespace":"tenant-gamma"},
		  "spec":{"replicas":3},
		  "status":{"readyReplicas":3,"availableReplicas":3}
		}`),
		mustObject(t, `{"kind":"Service","metadata":{"name":"company-site","namespace":"tenant-gamma"}}`),
		mustObject(t, `{
		  "kind":"Ingress",
		  "metadata":{"name":"company-site","namespace":"tenant-gamma"},
		  "spec":{"rules":[{"host":"gamma.gi.org"}]}
		}`),
	}

	checks := validateCompanySite("gamma", "tenant-gamma", "gamma.gi.org")(objects)
	for _, c := range checks {
		if c.Status != statusPass {
			t.Fatalf("check %s failed: %s", c.Name, c.Detail)
		}
	}
}

func TestValidateStorageClassesRejectsWrongDefault(t *testing.T) {
	checks := validateStorageClasses([]object{
		mustObject(t, `{"kind":"StorageClass","metadata":{"name":"local","annotations":{"storageclass.kubernetes.io/is-default-class":"true"}}}`),
	})
	if len(checks) != 1 || checks[0].Status != statusFail {
		t.Fatalf("storage check = %#v, want one failing check", checks)
	}
}

func mustObject(t *testing.T, raw string) object {
	t.Helper()
	var obj object
	if err := json.Unmarshal([]byte(raw), &obj); err != nil {
		t.Fatal(err)
	}
	return obj
}
