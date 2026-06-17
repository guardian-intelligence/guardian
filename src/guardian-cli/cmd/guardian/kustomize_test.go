package main

import (
	"strings"
	"testing"
)

func TestKustomizeRootsBuild(t *testing.T) {
	kubectl, err := kubectlPath()
	if err != nil {
		t.Fatalf("locate kubectl: %v", err)
	}
	for _, path := range []string{
		"src/k8s/bootstrap/cert-manager/base",
		"src/k8s/bootstrap/crossplane/base",
		"src/k8s/bootstrap/external-secrets/base",
		"src/k8s/bootstrap/seed-registry/base",
		"src/k8s/bootstrap/openbao/base",
		"src/k8s/bootstrap/provider-kubernetes/package",
		"src/k8s/bootstrap/provider-kubernetes/config",
		"src/k8s/bootstrap/local-storage/base",
		"src/k8s/reconciled/observability",
		"src/k8s/reconciled/observability/gatus/base",
		"src/k8s/reconciled/observability/otel-collector/base",
		"src/crossplane/packages/guardian-platform",
		"src/crossplane/packages/guardian-products",
		"src/crossplane/environments/dev",
		"src/crossplane/environments/gamma",
		"src/crossplane/environments/prod",
	} {
		t.Run(path, func(t *testing.T) {
			out, err := buildKustomization(kubectl, path, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(decodeKinds(t, out)) == 0 {
				t.Fatalf("kustomize root %s built no Kubernetes objects", path)
			}
		})
	}
}

func TestEnvironmentKustomizePatchesImages(t *testing.T) {
	for _, siteName := range []string{"dev", "gamma", "prod"} {
		t.Run(siteName, func(t *testing.T) {
			site := loadTestSite(t, siteName)
			out, err := buildTestEnvironmentBundle(site, testProductImages())
			if err != nil {
				t.Fatal(err)
			}
			text := string(out)
			for _, image := range []string{
				aisucksTestImage,
				companyTestImage,
				directusTestImage,
				postgresTestImage,
				statusTestImage,
				victoriaMetricsTestImage,
			} {
				if !strings.Contains(text, image) {
					t.Fatalf("environment kustomize output missing patched image %s", image)
				}
			}
			if siteUsesPlatformOCI(site) && !strings.Contains(text, zotTestImage) {
				t.Fatalf("environment kustomize output missing patched zot image %s", zotTestImage)
			}
		})
	}
}
