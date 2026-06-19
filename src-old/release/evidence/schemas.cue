package evidence

// Guardian-owned evidence contracts around standards-owned in-toto/SLSA
// predicates. The standard predicate schemas remain authoritative; this file
// names the release evidence surfaces Guardian emits and promotes on.

#Sha256Digest: =~"^[a-f0-9]{64}$"

#Digest: {
	sha256: #Sha256Digest
}

#ResourceRef: {
	uri:    string
	digest: #Digest
}

#Subject: {
	name:   string
	digest: #Digest
}

#Base64: =~"^[A-Za-z0-9+/]*={0,2}$"

#DSSESignature: {
	keyid?: string
	sig:    #Base64
}

#DSSEEnvelope: {
	payloadType: string
	payload:     #Base64
	signatures: [#DSSESignature, ...#DSSESignature]
}

#InTotoDSSEEnvelope: #DSSEEnvelope & {
	payloadType: "application/vnd.in-toto+json"
}

#GuardianTrack: "edge" | "nightly" | "rc" | "stable"

#GuardianVSAKind:
	"build" |
	"license" |
	"promotion" |
	"deployment"

#SlsaResult:
	"FAILED" |
	=~"^SLSA_[A-Z0-9_]+_LEVEL_(UNEVALUATED|[0-9]+)$"

#GuardianVSADetails: {
	kind:   #GuardianVSAKind
	track?: #GuardianTrack
}

#VSAPredicate: {
	verifier: {
		id: string
		version?: [string]: string
	}

	timeVerified: string
	resourceUri:  string
	policy:       #ResourceRef

	inputAttestations: [...#ResourceRef]

	verificationResult: "PASSED" | "FAILED"
	verifiedLevels: [#SlsaResult, ...#SlsaResult]
	slsaVersion: =~"^1\\.[0-9]+$"

	"https://guardianintelligence.org/evidence/v1"?: #GuardianVSADetails
}

#SlsaVSAStatement: {
	_type: "https://in-toto.io/Statement/v1"
	subject: [#Subject, ...#Subject]
	predicateType: "https://slsa.dev/verification_summary/v1"
	predicate:     #VSAPredicate
}

#GuardianVSAStatement: #SlsaVSAStatement & {
	predicate: {
		"https://guardianintelligence.org/evidence/v1": #GuardianVSADetails
	}
}
