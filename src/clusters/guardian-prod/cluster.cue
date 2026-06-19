package cluster

name: "guardian-prod"
domain: "guardianintelligence.org"
apiServerDomain: "api.guardianintelligence.org"

// Prod membership stays empty until each host has a refreshed host.cue and an
// explicit operator go. The excluded Verself prod host is not represented here.
members: []
environments: ["prod"]

network: {
	podCIDR: "10.244.0.0/16"
	serviceCIDR: "10.96.0.0/16"
	joinCIDR: "100.64.0.0/16"
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
	removeControlPlaneTaint: false
}

bootstrap: {
	destructive: false
	requireMaintenance: true
	targetState: "talos-maintenance"
	genesis: {
		ageRecipients: [
			"age1e95feklupyh40qa24vly650vg0qmljcsfhqd66fwhwa82j3uefnsxed3s8",
		]
	}
}
