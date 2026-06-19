package cluster

name: "guardian-nonprod"
domain: "guardianintelligence.org"
apiServerDomain: "api.nonprod.guardianintelligence.org"

members: ["ash-bm-001"]
environments: ["dev", "gamma"]

network: {
	podCIDR: "10.244.0.0/16"
	serviceCIDR: "10.96.0.0/16"
	joinCIDR: "100.64.0.0/16"
	advertisedCIDR: "206.223.228.100/31"
}

talos: {
	version: "v1.13.4"
	talmVersion: "v1.13"
	kubernetesVersion: "1.36.1"
	installerImage: "ghcr.io/cozystack/cozystack/talos:v1.13.0"
}

cozystack: {
	version: "1.4.1"
	variant: "isp-full"
	removeControlPlaneTaint: true
}

bootstrap: {
	destructive: true
	requireMaintenance: true
	targetState: "talos-maintenance"
	genesis: {
		ageRecipients: [
			"age1e95feklupyh40qa24vly650vg0qmljcsfhqd66fwhwa82j3uefnsxed3s8",
		]
	}
}
