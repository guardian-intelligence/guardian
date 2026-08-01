package tests

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const (
	pipeImageWorkflow   = ".github/workflows/pipe-to-remote-box-image.yml"
	pipeReleaseWorkflow = ".github/workflows/pipe-to-remote-box-release.yml"
	pipeNPMWorkflow     = ".github/workflows/pipe-to-remote-box-publish-npm.yml"
	pipeCratesWorkflow  = ".github/workflows/pipe-to-remote-box-publish-crates.yml"
	pipeChannelsFile    = "src/products/pipe-to-remote-box/release/channels.yaml"
	pipeInstallerFile   = "src/products/pipe-to-remote-box/dist/install.sh"
	pipeProbeFile       = "src/products/pipe-to-remote-box/src/remote_probe.sh"
	pipeDeeptestFile    = "src/infrastructure/deployments/guardian/promotion/pipe-to-remote-box-deeptest-runner.yaml"
	pipeInstallCanary   = "src/infrastructure/deployments/guardian/promotion/pipe-to-remote-box-install-canary.yaml"
	pipePromoterFile    = "src/infrastructure/deployments/guardian/promotion/pipe-to-remote-box-nightly-promoter.yaml"
	pipeStageFile       = "src/infrastructure/deployments/guardian/promotion/pipelines/products-pipe-to-remote-box-stage-nightly.yaml"
	pipeImageRepo       = "ghcr.io/guardian-intelligence/pipe-to-remote-box"
	pipeBuildIdentity   = "https://github.com/guardian-intelligence/guardian/.github/workflows/pipe-to-remote-box-image.yml@refs/heads/main"
	pipeJourneyImage    = "ghcr.io/guardian-intelligence/canary-journeys:edge@sha256:bad66489e28a4821ac3fc4782e9cbf7b1ee5855dc9a4ca2b407df7981bd1fbc1"
)

var pipeReleaseChannels = []string{"nightly", "rc", "stable"}

func TestPipeToRemoteBoxReleaseTargetsMoveTogether(t *testing.T) {
	targets := workflowTargetList(t, pipeImageWorkflow, "TARGETS")
	if len(targets) != 4 {
		t.Fatalf("%s builds %d targets, want the four-target public matrix", pipeImageWorkflow, len(targets))
	}
	assertSameSet(t, "the Pipe to Remote Box release cutter attaches", targets,
		workflowTargetList(t, pipeReleaseWorkflow, "TARGETS"))

	var npmTargets []string
	for _, pair := range workflowTargetList(t, pipeNPMWorkflow, "PLATFORMS") {
		target, _, ok := strings.Cut(pair, ":")
		if !ok {
			t.Fatalf("%s PLATFORMS entry %q is not <target>:<platform>", pipeNPMWorkflow, pair)
		}
		npmTargets = append(npmTargets, target)
	}
	assertSameSet(t, "the Pipe to Remote Box npm mirror packs", targets, npmTargets)
}

func TestPipeToRemoteBoxVersionAndPublicIdentifiersMoveTogether(t *testing.T) {
	manifest := readText(t, runfilePath("src/products/pipe-to-remote-box/Cargo.toml"))
	lock := readText(t, runfilePath("src/products/pipe-to-remote-box/Cargo.lock"))
	build := readText(t, runfilePath("src/products/pipe-to-remote-box/BUILD.bazel"))

	manifestVersion := requiredCapture(t, "Cargo.toml package version", manifest,
		`(?ms)^\[package\].*?^version\s*=\s*"([^"]+)"`)
	lockVersion := requiredCapture(t, "Cargo.lock Pipe package version", lock,
		`(?m)^name = "pipe-to-remote-box"\nversion = "([^"]+)"`)
	buildVersion := requiredCapture(t, "Bazel CARGO_PKG_VERSION", build,
		`CARGO_PKG_VERSION"\s*:\s*"([^"]+)"`)
	if manifestVersion != lockVersion || manifestVersion != buildVersion {
		t.Fatalf("Pipe version drift: Cargo.toml=%s Cargo.lock=%s BUILD.bazel=%s",
			manifestVersion, lockVersion, buildVersion)
	}

	for label, value := range map[string]string{
		"Cargo package": requiredCapture(t, "Cargo package name", manifest,
			`(?ms)^\[package\].*?^name\s*=\s*"([^"]+)"`),
		"Cargo binary": requiredCapture(t, "Cargo binary name", manifest,
			`(?ms)^\[\[bin\]\].*?^name\s*=\s*"([^"]+)"`),
		"OCI repository": workflowEnv(t, pipeImageWorkflow)["IMAGE_REPO"],
	} {
		want := "pipe-to-remote-box"
		if label == "OCI repository" {
			want = pipeImageRepo
		}
		if value != want {
			t.Errorf("%s is %q, want %q", label, value, want)
		}
	}

	if !strings.Contains(manifest,
		`pkg-url = "{ repo }/releases/download/pipe-to-remote-box%2Fv{ version }/pipe-to-remote-box-{ target }"`) {
		t.Error("cargo-binstall URL does not use the Pipe tag and asset contract")
	}

	const metaPath = "src/products/pipe-to-remote-box/dist/npm/pipe-to-remote-box/package.json"
	var npmMeta struct {
		Name                 string            `json:"name"`
		Bin                  map[string]string `json:"bin"`
		OptionalDependencies map[string]string `json:"optionalDependencies"`
	}
	if err := json.Unmarshal([]byte(readText(t, runfilePath(metaPath))), &npmMeta); err != nil {
		t.Fatalf("%s does not parse: %v", metaPath, err)
	}
	if npmMeta.Name != "@guardian-intelligence/pipe-to-remote-box" {
		t.Errorf("npm meta package is named %q", npmMeta.Name)
	}
	if npmMeta.Bin["pipe-to-remote-box"] != "bin/pipe-to-remote-box" {
		t.Errorf("npm meta package binary map is %#v", npmMeta.Bin)
	}

	var expectedPlatformPackages []string
	for _, pair := range workflowTargetList(t, pipeNPMWorkflow, "PLATFORMS") {
		_, suffix, _ := strings.Cut(pair, ":")
		expectedPlatformPackages = append(expectedPlatformPackages,
			"@guardian-intelligence/pipe-to-remote-box-"+suffix)
	}
	assertSameStringSet(t, "npm optional dependencies", expectedPlatformPackages, mapKeys(npmMeta.OptionalDependencies))

	release := readText(t, runfilePath(pipeReleaseWorkflow))
	for _, contract := range []string{
		`tag="pipe-to-remote-box/nightly-$day"`,
		`tag="pipe-to-remote-box/rc-$day"`,
		`tag="pipe-to-remote-box/v$version"`,
		`pipe-to-remote-box-$target.sigstore.json`,
		`prefix="pipe-to-remote-box-$built-src"`,
	} {
		if !strings.Contains(release, contract) {
			t.Errorf("release cutter lacks public identifier contract %q", contract)
		}
	}
	for _, workflow := range []string{pipeNPMWorkflow, pipeCratesWorkflow} {
		if !strings.Contains(readText(t, runfilePath(workflow)),
			`startsWith(github.event.release.tag_name, 'pipe-to-remote-box/v')`) {
			t.Errorf("%s does not restrict publication to Pipe stable tags", workflow)
		}
	}
}

func TestPipeToRemoteBoxReleaseSupplyChainIsComplete(t *testing.T) {
	image := readText(t, runfilePath(pipeImageWorkflow))
	release := readText(t, runfilePath(pipeReleaseWorkflow))
	npm := readText(t, runfilePath(pipeNPMWorkflow))
	crates := readText(t, runfilePath(pipeCratesWorkflow))

	for label, check := range map[string]struct {
		text string
		want []string
	}{
		"image builder": {image, []string{
			"cargo zigbuild --release --locked", "--remap-path-prefix=", "cosign sign-blob --yes",
			"cosign sign --yes", "cosign attest --yes --type spdxjson", "cosign verify-attestation --type spdxjson",
			"//src/products/pipe-to-remote-box/canary/sshd:sshd", "stage/_canary/sshd",
			"org.guardian.canary-fixture-digest", "org.guardian.remote-probe-digest",
			"org.guardian.canary-notices-digest",
			"org.guardian.license-digest", "cp Cargo.toml Cargo.lock LICENSE sbom-input/",
			"golang.org/x/crypto", "golang.org/x/sys",
		}},
		"release cutter": {release, []string{
			"cosign verify-attestation --type spdxjson", "cosign verify-blob", "install.sh.sigstore.json",
			"org.guardian.license-digest", `cmp -s "stage/$channel/LICENSE" "assets/$channel/LICENSE"`,
			`grep -qxF "$prefix/LICENSE"`, `grep -qxF "$prefix/THIRD_PARTY_LICENSES.md"`,
			"sha256sum pipe-to-remote-box-* install.sh* LICENSE", "THIRD_PARTY_LICENSES.md > checksums.txt",
			"PROMOTIONS_APP_ID", "PROMOTIONS_APP_PRIVATE_KEY", "permission-contents: write",
			"select(.draft == false)",
			`gh release create "$tag" --draft --verify-tag`, `gh release upload "$tag" --clobber`,
			`gh release edit "$tag" --draft=false "${flags[@]}"`, "guardian-promotions[bot]",
		}},
		"npm mirror": {npm, []string{
			"Bind the release tag to a signed main build", "cosign verify --certificate-identity",
			"cosign verify-attestation --type spdxjson", "org.opencontainers.image.revision",
			"sha256sum -c checksums.txt", "cosign verify-blob", "npm publish --provenance --access public",
		}},
		"crates mirror": {crates, []string{
			"Bind the release tag to a signed main build", "cosign verify --certificate-identity", "org.opencontainers.image.revision",
			"rust-lang/crates-io-auth-action@", "cargo run --locked --release --quiet -- --version",
			"cargo package --locked", "cargo publish --locked -p pipe-to-remote-box",
		}},
	} {
		for _, want := range check.want {
			if !strings.Contains(check.text, want) {
				t.Errorf("%s lacks %q", label, want)
			}
		}
	}

	if !strings.Contains(image, pipeBuildIdentity) || !strings.Contains(release, pipeBuildIdentity) || !strings.Contains(npm, pipeBuildIdentity) {
		t.Errorf("builder, release cutter, and npm consumer do not pin one canonical build identity")
	}
	if strings.Contains(npm, "NODE_AUTH_TOKEN") || strings.Contains(crates, "CARGO_REGISTRY_TOKEN: ${{ secrets.") {
		t.Error("registry mirrors must use OIDC trusted publishing, not standing token secrets")
	}
	if strings.Contains(release, "_canary/sshd") || strings.Contains(npm, "_canary/sshd") {
		t.Error("the loopback SSH fixture is internal to the signed OCI and must not enter GitHub/npm release assets")
	}
	for _, workflow := range []struct {
		name string
		text string
		exec string
	}{
		{name: "npm", text: npm, exec: `"$PACKAGE_ROOT/stage-packages.sh"`},
		{name: "crates", text: crates, exec: "cargo metadata"},
	} {
		if !strings.Contains(workflow.text, `ref: ${{ github.event.release.tag_name }}`) ||
			!strings.Contains(workflow.text, "persist-credentials: false") {
			t.Errorf("%s publication does not check out the credential-free release tag", workflow.name)
		}
		bound := strings.Index(workflow.text, "tag_revision=\"$(git rev-parse HEAD)\"")
		firstExecution := strings.Index(workflow.text, workflow.exec)
		if bound < 0 || firstExecution < 0 || bound > firstExecution {
			t.Errorf("%s publication executes tagged product source before binding the tag SHA to the signed build revision", workflow.name)
		}
	}
	drafted := strings.Index(release, `gh release create "$tag" --draft --verify-tag`)
	authorChecked := strings.Index(release, `jq -r .author <<<"$release_json"`)
	draftRepaired := strings.Index(release, `gh release edit "$tag" --draft --verify-tag`)
	uploaded := strings.Index(release, `gh release upload "$tag" --clobber`)
	readBack := strings.Index(release, `cmp -s "assets/$channel/$asset_name" "$downloaded/$asset_name"`)
	published := strings.Index(release, `gh release edit "$tag" --draft=false "${flags[@]}"`)
	if drafted < 0 || uploaded < drafted || readBack < uploaded || published < readBack {
		t.Error("release cutter does not keep the Release draft through exact asset upload and byte readback")
	}
	if authorChecked < 0 || draftRepaired < authorChecked {
		t.Error("release cutter mutates a colliding draft before validating its App author")
	}

	advance := `crane tag "$image" edge`
	if strings.Count(image, advance) != 1 {
		t.Fatalf("image builder has %d edge-advance commands, want exactly one", strings.Count(image, advance))
	}
	verified := strings.Index(image, "Verify the complete consumer chain")
	advanced := strings.Index(image, advance)
	if verified < 0 || advanced < verified {
		t.Error("image builder advances edge before the immutable candidate's complete verification")
	}
	dedup := requiredCapture(t, "verified image dedup block", image,
		`(?ms)- name: Compare built bytes with the current edge\n(.*?)- name: Sign each binary`)
	for _, required := range []string{
		"cosign verify --certificate-identity", "cosign verify-attestation --type spdxjson",
		"cosign verify-blob", "cmp target/canary/sshd", "org.guardian.remote-probe-digest",
		"org.guardian.license-digest", "cmp LICENSE \"$verify_dir/LICENSE\"",
	} {
		if !strings.Contains(dedup, required) {
			t.Errorf("edge dedup can skip a rebuild without %q", required)
		}
	}
}

func TestPipeToRemoteBoxPromotionAndCanariesBindTheVerifiedArtifact(t *testing.T) {
	image := readText(t, runfilePath(pipeImageWorkflow))
	deeptest := readText(t, runfilePath(pipeDeeptestFile))
	install := readText(t, runfilePath(pipeInstallCanary))
	stage := readText(t, runfilePath(pipeStageFile))
	promoter := readText(t, runfilePath(pipePromoterFile))
	probe := readText(t, runfilePath(pipeProbeFile))
	probeDigest := fmt.Sprintf("%x", sha256.Sum256([]byte(probe)))
	license := readText(t, runfilePath("src/products/pipe-to-remote-box/LICENSE"))
	licenseDigest := fmt.Sprintf("%x", sha256.Sum256([]byte(license)))

	for label, text := range map[string]string{"image builder": image, "deep test": deeptest, "install canary": install} {
		for _, required := range []string{
			"org.guardian.canary-fixture-digest", "org.guardian.canary-notices-digest",
			"org.guardian.remote-probe-digest", "org.guardian.binaries-digest",
			"org.guardian.license-digest",
		} {
			if !strings.Contains(text, required) {
				t.Errorf("%s lacks the shared %s binding", label, required)
			}
		}
	}
	if !strings.Contains(deeptest, "EXPECTED_PROBE_SHA256="+probeDigest) {
		t.Errorf("deep test does not pin the embedded remote probe digest %s", probeDigest)
	}
	if !strings.Contains(install, "EXPECTED_REMOTE_PROBE_DIGEST="+probeDigest) {
		t.Errorf("install canary does not pin the embedded remote probe digest %s", probeDigest)
	}
	if !strings.Contains(deeptest, "EXPECTED_LICENSE_SHA256="+licenseDigest) {
		t.Errorf("deep test does not pin the shipped Apache-2.0 license digest %s", licenseDigest)
	}
	for label, text := range map[string]string{"image builder": image, "deep test": deeptest, "install canary": install} {
		if !strings.Contains(text, "LICENSE") {
			t.Errorf("%s does not include LICENSE in the signed OCI contract", label)
		}
	}
	if !strings.Contains(install, `cmp -s "$oci_dir/LICENSE" "$assets_dir/LICENSE"`) {
		t.Error("public install canary does not bind the OCI license bytes to the checksum-verified release license")
	}
	for label, text := range map[string]string{"deep test": deeptest, "install canary": install} {
		if !strings.Contains(text, "image: "+pipeJourneyImage) {
			t.Errorf("%s does not use the approved digest-pinned journey runtime", label)
		}
		if strings.Contains(text, "image: mcr.microsoft.com/") {
			t.Errorf("%s bypasses the repository's image provenance allowlist", label)
		}
		if !strings.Contains(text, "automountServiceAccountToken: false") {
			t.Errorf("%s unexpectedly receives a Kubernetes API token", label)
		}
	}

	for _, required := range []string{
		"verify --new-bundle-format=false", "verify-attestation --new-bundle-format=false --type spdxjson",
		"verify-blob", "PATH=/usr/bin:/bin", `command -v ssh)" = /usr/bin/ssh`,
		"strict_host_key", "fixed_probe", "directory_boundaries", "missing_directory",
		"not_directory", "timeout", "no_mutation",
	} {
		if !strings.Contains(deeptest+install, required) {
			t.Errorf("real-SSH canary contract lacks %q", required)
		}
	}
	if strings.Contains(deeptest, "ProxyCommand=") || strings.Contains(install, "ProxyCommand=") {
		t.Error("canaries must not replace OpenSSH with a command shim")
	}

	for _, required := range []string{
		`guardian_pipe_to_remote_box_deeptest_pass{digest=`,
		`imageSelectionStrategy: Digest`, `constraint: edge`,
		`guardian_pipe_to_remote_box_deeptest_pass{digest="${{ imageFrom(vars.imageRepo).Digest }}"}`,
		"src/products/pipe-to-remote-box/release/channels.yaml",
		"src/infrastructure/deployments/guardian/system/release-manifest.yaml",
	} {
		if !strings.Contains(deeptest+stage, required) {
			t.Errorf("digest-bound nightly gate lacks %q", required)
		}
	}
	for _, required := range []string{
		`resourceNames: ["pipe-to-remote-box-nightly"]`, "SOAK_SECONDS=3600",
		"DEPARTURE_SECONDS=21600", `select(.status.phase=="Pending" or .status.phase=="Running")`,
		"stage template defines no steps; refusing to promote", "kubectl create -f -",
	} {
		if !strings.Contains(promoter, required) {
			t.Errorf("bounded nightly promoter lacks %q", required)
		}
	}

	for _, required := range []string{
		"release-assets installer", `methods="$methods npm crates"`,
		"guardian_pipe_to_remote_box_install_canary_active_lanes",
		"GuardianPipeToRemoteBoxInstallCanaryFailing",
		"GuardianPipeToRemoteBoxInstallCanarySilent",
	} {
		if !strings.Contains(install, required) {
			t.Errorf("public distribution canary lacks %q", required)
		}
	}
	for _, forbidden := range []string{"PROMOTIONS_APP_PRIVATE_KEY", "NODE_AUTH_TOKEN", "CARGO_REGISTRY_TOKEN", "gh issue"} {
		if strings.Contains(install, forbidden) {
			t.Errorf("public distribution canary contains privileged capability %q", forbidden)
		}
	}
}

func TestPipeToRemoteBoxBinaryDistributionsCarryRequiredNotices(t *testing.T) {
	notices := readText(t, runfilePath("src/products/pipe-to-remote-box/THIRD_PARTY_LICENSES.md"))
	for _, required := range []string{
		"Copyright (c) 2015 Danny Guo",
		"Copyright (c) 2016 Titus Wormer <tituswormer@gmail.com>",
		"Copyright (c) 2018 Akash Kurdekar",
		"UNICODE LICENSE V3",
		"Copyright © 1991-2023 Unicode, Inc.",
		"Copyright 2009 The Go Authors.",
	} {
		if !strings.Contains(notices, required) {
			t.Errorf("third-party notices omit %q", required)
		}
	}
	goMod := readText(t, runfilePath("go.mod"))
	cryptoVersion := requiredCapture(t, "golang.org/x/crypto version", goMod,
		`(?m)^\s*golang\.org/x/crypto\s+v([0-9]+\.[0-9]+\.[0-9]+)\s*$`)
	sysVersion := requiredCapture(t, "golang.org/x/sys version", goMod,
		`(?m)^\s*golang\.org/x/sys\s+v([0-9]+\.[0-9]+\.[0-9]+)\s*$`)
	if !strings.Contains(notices, "golang.org/x/crypto "+cryptoVersion+" and golang.org/x/sys "+sysVersion) {
		t.Error("Go canary-fixture notice versions drift from go.mod")
	}

	manifest := readText(t, runfilePath("src/products/pipe-to-remote-box/Cargo.toml"))
	if !strings.Contains(manifest, `"/THIRD_PARTY_LICENSES.md"`) {
		t.Error("crates.io source package does not include the third-party notices")
	}
	npm := readText(t, runfilePath(pipeNPMWorkflow))
	if !strings.Contains(npm, `"$PWD/assets/LICENSE" "$PWD/assets/THIRD_PARTY_LICENSES.md"`) {
		t.Error("npm staging does not consume the checksum-verified release notices")
	}
	installCanary := readText(t, runfilePath(pipeInstallCanary))
	if !strings.Contains(installCanary, `grep -qxF 'Copyright 2009 The Go Authors.'`) {
		t.Error("public install canary does not verify the exact shipped Go copyright notice")
	}
}

func TestPipeToRemoteBoxChannelGrammarMovesTogether(t *testing.T) {
	release := readText(t, runfilePath(pipeReleaseWorkflow))
	installer := readText(t, runfilePath(pipeInstallerFile))

	releasePatterns := map[string]string{}
	for _, match := range regexp.MustCompile(`(?m)^\s*(nightly|rc|stable)\) match='test\("([^"]+)"\)' ;;$`).FindAllStringSubmatch(release, -1) {
		// The jq source holds a JSON string, so its doubled backslashes become
		// one backslash before the regular-expression engine sees them.
		releasePatterns[match[1]] = strings.ReplaceAll(match[2], `\\`, `\`)
	}

	installerPatterns := map[string]string{}
	installerBlocks := map[string]string{}
	for _, match := range regexp.MustCompile(`(?ms)^\s*(nightly|rc|stable)\)\n(.*?)^\s*;;$`).FindAllStringSubmatch(installer, -1) {
		installerBlocks[match[1]] = match[2]
		pattern := regexp.MustCompile(`grep -Eq "([^"]+)"`).FindStringSubmatch(match[2])
		if pattern != nil {
			installerPatterns[match[1]] = strings.Replace(pattern[1], `^$TAG_PREFIX`, `^pipe-to-remote-box`, 1)
		}
	}

	for _, channel := range pipeReleaseChannels {
		if releasePatterns[channel] == "" || installerPatterns[channel] == "" {
			t.Errorf("channel %s has no readable matcher in both cutter and installer", channel)
			continue
		}
		if releasePatterns[channel] != installerPatterns[channel] {
			t.Errorf("channel %s grammar differs: cutter %q, installer %q", channel, releasePatterns[channel], installerPatterns[channel])
		}
		wantPrerelease := `[ "$3" = true ]`
		if channel == "stable" {
			wantPrerelease = `[ "$3" = false ]`
		}
		if !strings.Contains(installerBlocks[channel], wantPrerelease) {
			t.Errorf("installer channel %s does not enforce %s", channel, wantPrerelease)
		}
	}

	for _, required := range []string{
		`flags=(--prerelease --latest=false)`,
		`flags=(--prerelease=false --latest=false)`,
	} {
		if !strings.Contains(release, required) {
			t.Errorf("release cutter lacks %q", required)
		}
	}
}

func TestPipeToRemoteBoxChannelsAndReleaseManifestMoveTogether(t *testing.T) {
	type lane struct {
		Image   string `yaml:"image"`
		Version string `yaml:"version"`
	}
	var channelsDoc struct {
		Channels map[string]lane `yaml:"channels"`
	}
	if err := yaml.Unmarshal([]byte(readText(t, runfilePath(pipeChannelsFile))), &channelsDoc); err != nil {
		t.Fatalf("%s does not parse: %v", pipeChannelsFile, err)
	}

	var manifestDoc struct {
		Releases map[string]map[string]lane `yaml:"releases"`
	}
	if err := yaml.Unmarshal([]byte(readText(t, runfilePath(releaseManifestRunfile))), &manifestDoc); err != nil {
		t.Fatalf("%s does not parse: %v", releaseManifestRunfile, err)
	}
	manifest, ok := manifestDoc.Releases["pipe-to-remote-box"]
	if !ok {
		t.Fatalf("%s has no releases.pipe-to-remote-box lane", releaseManifestRunfile)
	}

	assertSameStringSet(t, pipeChannelsFile+" declares channels", pipeReleaseChannels, mapKeys(channelsDoc.Channels))
	for _, channel := range pipeReleaseChannels {
		pin := channelsDoc.Channels[channel]
		projected := manifest[channel]
		if pin.Image != projected.Image {
			t.Errorf("%s %s pin %q differs from release manifest %q", pipeChannelsFile, channel, pin.Image, projected.Image)
		}
		if pin.Image == "" {
			continue
		}
		if !regexp.MustCompile(`^` + regexp.QuoteMeta(pipeImageRepo) + `@sha256:[a-f0-9]{64}$`).MatchString(pin.Image) {
			t.Errorf("%s %s pin %q is not a digest-pinned %s artifact", pipeChannelsFile, channel, pin.Image, pipeImageRepo)
		}
		if channel != "nightly" && pin.Version == "" {
			t.Errorf("%s %s pin has no version", pipeChannelsFile, channel)
		}
	}
}

func TestPipeToRemoteBoxCountersignerPinsBuilderIdentity(t *testing.T) {
	countersigner := readText(t, runfilePath("src/infrastructure/deployments/guardian/system/zot-countersigner.yaml"))
	arm := regexp.MustCompile(`(?s)pipe-to-remote-box\)\s+identity="([^"]+)"\s+;;`).FindStringSubmatch(countersigner)
	if arm == nil {
		t.Fatal("countersigner has no pipe-to-remote-box identity arm")
	}
	if arm[1] != pipeBuildIdentity {
		t.Fatalf("countersigner trusts %q for Pipe to Remote Box, want %q", arm[1], pipeBuildIdentity)
	}
}

func mapKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return keys
}

func assertSameStringSet(t *testing.T, context string, want, got []string) {
	t.Helper()
	want = append([]string(nil), want...)
	got = append([]string(nil), got...)
	sort.Strings(want)
	sort.Strings(got)
	if strings.Join(want, " ") != strings.Join(got, " ") {
		t.Fatalf("%s [%s], want [%s]", context, strings.Join(got, " "), strings.Join(want, " "))
	}
}

func requiredCapture(t *testing.T, label, text, expression string) string {
	t.Helper()
	match := regexp.MustCompile(expression).FindStringSubmatch(text)
	if match == nil {
		t.Fatalf("could not read %s", label)
	}
	return match[1]
}
