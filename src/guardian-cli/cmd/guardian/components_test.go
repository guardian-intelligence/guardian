package main

import "testing"

// TestComponentsTable pins the structural invariants of the components
// table that nothing in the type system enforces:
//
//   - a manifest-only component (layout == "") MUST be deliberately gated
//     (enabled != nil): with no image and no site gate there would be
//     nothing deliberate about where its objects land;
//   - the gateway entry applies after every component whose namespace its
//     routes live in (aisucks, status) — applying a route into a namespace
//     that does not exist yet fails the converge, and app-then-gateway is
//     also the cutover's overlap ordering (docs/architecture/gateway.md).
func TestComponentsTable(t *testing.T) {
	indexOf := make(map[string]int, len(components))
	for i, c := range components {
		if c.name == "" || c.manifest == "" {
			t.Errorf("components[%d]: name and manifest are required (got name=%q manifest=%q)", i, c.name, c.manifest)
		}
		if _, dup := indexOf[c.name]; dup {
			t.Errorf("components: duplicate name %q", c.name)
		}
		indexOf[c.name] = i
		if c.layout == "" && c.enabled == nil {
			t.Errorf("component %q is manifest-only (no layout) but ungated: manifest-only components must set enabled", c.name)
		}
	}
	gw, ok := indexOf["gateway"]
	if !ok {
		t.Fatal("components: gateway entry missing")
	}
	for _, dep := range []string{"aisucks", "status"} {
		di, ok := indexOf[dep]
		if !ok {
			t.Fatalf("components: %s entry missing", dep)
		}
		if gw < di {
			t.Errorf("components: gateway (index %d) must apply after %s (index %d): its routes live in the %s namespace", gw, dep, di, dep)
		}
	}
}
