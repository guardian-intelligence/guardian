package tests

import (
	"encoding/binary"
	"net"
	"testing"
)

func TestStageTenantHostnameOwnership(t *testing.T) {
	path := runfilePath("src/infrastructure/deployments/guardian/system/stage-tenants.yaml")
	docs := yamlDocs(t, path)

	prod := findDoc(t, docs, "Tenant", "prod")
	assertNestedString(t, prod, "guardianintelligence.org", "spec", "host")

	previews := findDoc(t, docs, "Tenant", "previews")
	assertNestedString(t, previews, "previews.guardianintelligence.org", "spec", "host")
}

func TestVPAForVPABootstrapMemoryFloor(t *testing.T) {
	path := runfilePath("src/infrastructure/base/platform-patches/cozystack-vpa-for-vpa.yaml")
	vpa := singleYAMLDoc(t, path)

	policies := sliceValue(nestedValue(t, vpa, "spec", "resourcePolicy", "containerPolicies"))
	if len(policies) != 1 {
		t.Fatalf("spec.resourcePolicy.containerPolicies has %d entries, want 1", len(policies))
	}
	recommender := mapValue(policies[0])
	assertNestedString(t, recommender, "recommender", "containerName")
	assertNestedString(t, recommender, "1Gi", "minAllowed", "memory")
}

// Flux owns ovn-default's excludeIps, so the list must reserve the block the
// Cilium IPAM migration hook (Cozystack v1.6.5+) adds there: the last /21 of
// networking.podCIDR. Without it every Flux apply reverts the hook's
// exclusion and kube-ovn can hand pods addresses Cilium holds.
func TestOvnDefaultExcludesCiliumPool(t *testing.T) {
	platform := singleYAMLDoc(t, runfilePath("src/infrastructure/base/cozystack/platform.yaml"))
	podCIDR, ok := nestedValue(t, platform,
		"spec", "components", "platform", "values", "networking", "podCIDR").(string)
	if !ok {
		t.Fatalf("platform networking.podCIDR is not a string")
	}
	_, network, err := net.ParseCIDR(podCIDR)
	if err != nil {
		t.Fatalf("platform networking.podCIDR %q: %v", podCIDR, err)
	}
	ones, bits := network.Mask.Size()
	if bits != 32 || ones > 20 {
		t.Fatalf("networking.podCIDR %s must be IPv4 /20 or wider on the kube-ovn variants", podCIDR)
	}
	base := binary.BigEndian.Uint32(network.IP.To4())
	first := base + (uint32(1) << (32 - ones)) - 2048
	ip := func(v uint32) string {
		b := make([]byte, 4)
		binary.BigEndian.PutUint32(b, v)
		return net.IP(b).String()
	}
	want := ip(first) + ".." + ip(first+2047)

	docs := yamlDocs(t, runfilePath("src/infrastructure/base/networking/subnet-mtu.yaml"))
	subnet := findDoc(t, docs, "Subnet", "ovn-default")
	assertNestedString(t, subnet, podCIDR, "spec", "cidrBlock")
	for _, entry := range sliceValue(nestedValue(t, subnet, "spec", "excludeIps")) {
		if entry == want {
			return
		}
	}
	t.Fatalf("ovn-default spec.excludeIps must contain the Cilium pool %s", want)
}
