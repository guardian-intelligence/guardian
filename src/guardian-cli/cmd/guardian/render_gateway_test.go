package main

import (
	"bytes"
	"io"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestGatewayManifestRender renders the manifest-only gateway component for
// each real site.yaml and pins the route topology:
//
//   - the Gateway (namespace gateway) carries hostname-scoped :443
//     TLS-passthrough listeners, an optional platform HTTPS listener, and the
//     :80 HTTP listener;
//   - one TLSRoute per SNI surface, at v1alpha2 — the pin of record: Cilium
//     1.19 consumes TLSRoute at v1alpha2 only (see the header of
//     gateway-api-crds-inline.yaml); a v1 TLSRoute would be silently
//     ignored;
//   - the aisucks HTTPRoute has NO hostnames: :80 must answer raw-IP Host
//     headers (the gatus no-domain fallback and ACME HTTP-01 / operator
//     curls must not depend on Host);
//   - status TLSRoutes exist exactly where site.yaml declares
//     status.domains (dev, gamma — not prod);
//   - oci.domain renders cert-manager resources and an HTTPS HTTPRoute on dev;
//   - the template never references .Image: it renders with the empty
//     image a manifest-only component gets.
func TestGatewayManifestRender(t *testing.T) {
	tmpl, err := toolPath("_main/src/infrastructure-components/gateway/k8s/gateway.yaml.tmpl")
	if err != nil {
		t.Fatalf("locate gateway manifest: %v", err)
	}
	wantStatusDomains := map[string][]string{
		"dev":   {"status.guardianintelligence.org", "status.dev.guardianintelligence.org"},
		"gamma": {"status.gamma.guardianintelligence.org"},
		"prod":  nil,
	}
	wantAisucksDomain := map[string]string{
		"dev":   "dev.aisucks.app",
		"gamma": "gamma.aisucks.app",
		"prod":  "aisucks.app",
	}
	wantOCIDomain := map[string]string{
		"dev": "oci.guardianintelligence.org",
	}
	for _, siteName := range []string{"dev", "gamma", "prod"} {
		t.Run(siteName, func(t *testing.T) {
			sitePath, err := toolPath("_main/src/sites/" + siteName + "/site.yaml")
			if err != nil {
				t.Fatalf("locate site.yaml: %v", err)
			}
			site, err := loadSite(sitePath)
			if err != nil {
				t.Fatal(err)
			}
			// Manifest-only: rendered with the zero image, like up.go does.
			rendered, err := renderManifest(tmpl, "", site)
			if err != nil {
				t.Fatal(err)
			}
			out := string(rendered)
			if strings.Contains(out, "image:") {
				t.Error("gateway manifest must never reference an image: it is manifest-only")
			}
			if wantOCIDomain[siteName] != "" {
				if strings.Contains(out, "http01:") {
					t.Error("platform TLS must not use HTTP-01: cert-manager self-check cannot reach the site's public IP from inside the cluster")
				}
				if !strings.Contains(out, "dns01:") || !strings.Contains(out, "cloudflare-guardianintelligence-org-dns-token") {
					t.Error("platform TLS must use Cloudflare DNS-01 with the cert-manager token Secret")
				}
			}

			docs := decodeGatewayDocs(t, rendered)
			wantKinds := []string{"Namespace", "Gateway", "TLSRoute", "HTTPRoute"}
			if len(wantStatusDomains[siteName]) > 0 {
				wantKinds = append(wantKinds, "TLSRoute")
			}
			if wantOCIDomain[siteName] != "" {
				wantKinds = append(wantKinds, "ClusterIssuer", "Certificate", "HTTPRoute")
			}
			var kinds []string
			for _, d := range docs {
				kinds = append(kinds, d.Kind)
			}
			if strings.Join(kinds, ",") != strings.Join(wantKinds, ",") {
				t.Fatalf("kinds = %v; want %v", kinds, wantKinds)
			}

			for _, d := range docs {
				switch {
				case d.Kind == "Gateway":
					if d.APIVersion != "gateway.networking.k8s.io/v1" {
						t.Errorf("Gateway apiVersion = %s; want v1", d.APIVersion)
					}
					assertGatewayListeners(t, d, wantAisucksDomain[siteName], wantStatusDomains[siteName], wantOCIDomain[siteName])
				case d.Kind == "TLSRoute":
					// THE CRD pin: Cilium 1.19 watches TLSRoute at v1alpha2.
					if d.APIVersion != "gateway.networking.k8s.io/v1alpha2" {
						t.Errorf("TLSRoute %s/%s apiVersion = %s; want v1alpha2 (Cilium 1.19 silently ignores v1)", d.Metadata.Namespace, d.Metadata.Name, d.APIVersion)
					}
				case d.Kind == "HTTPRoute":
					if d.Metadata.Name == "aisucks-http" && len(d.Spec.Hostnames) != 0 {
						t.Errorf("HTTPRoute must stay hostname-less (raw-IP Host headers: gatus fallback, HTTP-01); got %v", d.Spec.Hostnames)
					}
				}
				if d.Kind == "TLSRoute" && d.Metadata.Name == "aisucks" {
					if len(d.Spec.Hostnames) != 1 || d.Spec.Hostnames[0] != wantAisucksDomain[siteName] {
						t.Errorf("aisucks TLSRoute hostnames = %v; want [%s]", d.Spec.Hostnames, wantAisucksDomain[siteName])
					}
					if got := sectionNames(d.Spec.ParentRefs); strings.Join(got, ",") != "tls-aisucks" {
						t.Errorf("aisucks TLSRoute parent sectionNames = %v; want [tls-aisucks]", got)
					}
				}
				if d.Kind == "TLSRoute" && d.Metadata.Name == "status" {
					if strings.Join(d.Spec.Hostnames, ",") != strings.Join(wantStatusDomains[siteName], ",") {
						t.Errorf("status TLSRoute hostnames = %v; want %v", d.Spec.Hostnames, wantStatusDomains[siteName])
					}
					if d.Metadata.Namespace != "status" {
						t.Errorf("status TLSRoute namespace = %s; want status (same-namespace backendRef, no ReferenceGrant)", d.Metadata.Namespace)
					}
					var wantSections []string
					for i := range wantStatusDomains[siteName] {
						wantSections = append(wantSections, "tls-status-"+strconv.Itoa(i))
					}
					if got := sectionNames(d.Spec.ParentRefs); strings.Join(got, ",") != strings.Join(wantSections, ",") {
						t.Errorf("status TLSRoute parent sectionNames = %v; want %v", got, wantSections)
					}
				}
				if d.Kind == "Certificate" {
					if d.Metadata.Namespace != "gateway" {
						t.Errorf("Certificate namespace = %s; want gateway", d.Metadata.Namespace)
					}
					if strings.Join(d.Spec.DNSNames, ",") != wantOCIDomain[siteName] {
						t.Errorf("Certificate dnsNames = %v; want [%s]", d.Spec.DNSNames, wantOCIDomain[siteName])
					}
				}
				if d.Kind == "HTTPRoute" && d.Metadata.Name == "guardian-oci" {
					if d.Metadata.Namespace != "guardian-oci" {
						t.Errorf("guardian-oci HTTPRoute namespace = %s; want guardian-oci", d.Metadata.Namespace)
					}
					if strings.Join(d.Spec.Hostnames, ",") != wantOCIDomain[siteName] {
						t.Errorf("guardian-oci HTTPRoute hostnames = %v; want [%s]", d.Spec.Hostnames, wantOCIDomain[siteName])
					}
					if got := sectionNames(d.Spec.ParentRefs); strings.Join(got, ",") != "https-oci" {
						t.Errorf("guardian-oci HTTPRoute parent sectionNames = %v; want [https-oci]", got)
					}
				}
			}
		})
	}
}

// gatewayDoc is the slice of each rendered object the render test asserts.
type gatewayDoc struct {
	APIVersion string `yaml:"apiVersion"`
	Kind       string `yaml:"kind"`
	Metadata   struct {
		Name      string `yaml:"name"`
		Namespace string `yaml:"namespace"`
	} `yaml:"metadata"`
	Spec struct {
		Hostnames []string `yaml:"hostnames"`
		DNSNames  []string `yaml:"dnsNames"`
		Listeners []struct {
			Name     string `yaml:"name"`
			Hostname string `yaml:"hostname"`
			Port     int    `yaml:"port"`
			Protocol string `yaml:"protocol"`
			TLS      struct {
				Mode            string `yaml:"mode"`
				CertificateRefs []struct {
					Name string `yaml:"name"`
				} `yaml:"certificateRefs"`
			} `yaml:"tls"`
		} `yaml:"listeners"`
		ParentRefs []struct {
			SectionName string `yaml:"sectionName"`
		} `yaml:"parentRefs"`
	} `yaml:"spec"`
}

func sectionNames(refs []struct {
	SectionName string `yaml:"sectionName"`
}) []string {
	var out []string
	for _, ref := range refs {
		out = append(out, ref.SectionName)
	}
	return out
}

func assertGatewayListeners(t *testing.T, d gatewayDoc, aisucks string, status []string, oci string) {
	t.Helper()
	listeners := map[string]struct {
		Hostname string
		Port     int
		Protocol string
		TLSMode  string
		CertName string
	}{}
	for _, l := range d.Spec.Listeners {
		got := struct {
			Hostname string
			Port     int
			Protocol string
			TLSMode  string
			CertName string
		}{Hostname: l.Hostname, Port: l.Port, Protocol: l.Protocol, TLSMode: l.TLS.Mode}
		if len(l.TLS.CertificateRefs) > 0 {
			got.CertName = l.TLS.CertificateRefs[0].Name
		}
		listeners[l.Name] = got
	}
	assertListener(t, listeners, "tls-aisucks", aisucks, 443, "TLS", "Passthrough", "")
	for i, domain := range status {
		assertListener(t, listeners, "tls-status-"+strconv.Itoa(i), domain, 443, "TLS", "Passthrough", "")
	}
	if oci != "" {
		assertListener(t, listeners, "https-oci", oci, 443, "HTTPS", "Terminate", "oci-guardianintelligence-org-tls")
	}
	assertListener(t, listeners, "http", "", 80, "HTTP", "", "")
}

func assertListener(t *testing.T, listeners map[string]struct {
	Hostname string
	Port     int
	Protocol string
	TLSMode  string
	CertName string
}, name, hostname string, port int, protocol, tlsMode, certName string) {
	t.Helper()
	got, ok := listeners[name]
	if !ok {
		t.Fatalf("Gateway listener %s missing; got listeners %v", name, listeners)
	}
	if got.Hostname != hostname || got.Port != port || got.Protocol != protocol || got.TLSMode != tlsMode || got.CertName != certName {
		t.Errorf("Gateway listener %s = %+v; want hostname=%q port=%d protocol=%s tlsMode=%q certName=%q", name, got, hostname, port, protocol, tlsMode, certName)
	}
}

func decodeGatewayDocs(t *testing.T, manifest []byte) []gatewayDoc {
	t.Helper()
	dec := yaml.NewDecoder(bytes.NewReader(manifest))
	var docs []gatewayDoc
	for {
		var d gatewayDoc
		if err := dec.Decode(&d); err == io.EOF {
			break
		} else if err != nil {
			t.Fatalf("rendered gateway manifest is not valid YAML: %v\n%s", err, manifest)
		}
		if d.Kind != "" {
			docs = append(docs, d)
		}
	}
	return docs
}
