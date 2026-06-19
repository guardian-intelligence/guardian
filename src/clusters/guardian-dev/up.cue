package cluster

cluster: {
	name: "guardian-dev"
	endpoint: "https://206.223.228.101:6443"
	domain: "guardianintelligence.org"
	apiServerDomain: "api.dev.guardianintelligence.org"
	podCIDR: "10.244.0.0/16"
	serviceCIDR: "10.96.0.0/16"
	joinCIDR: "100.64.0.0/16"
	advertisedCIDR: "206.223.228.100/31"
}

provider: {
	name: "latitude"
	serverId: "sv_vAPXaMxKM5epz"
	tokenEnv: "LATITUDE_API_KEY"
	reinstall: true
	talosSchematic: "talos/schematic.yaml"
	talosVersion: "v1.13.4"
	refuseProdNames: true
}

node: {
	name: "ash-bm-001"
	address: "206.223.228.101"
	hostname: "gi-ash-dev-platform-01"
	interfaceMac: "90:5a:08:33:ba:9f"
	installDiskSerial: "362510FCEFB8"
	role: "control-plane"
}

talm: {
	preset: "cozystack"
	talosVersion: "v1.13"
	kubernetesVersion: "1.36.1"
	installerImage: "ghcr.io/cozystack/cozystack/talos:v1.13.0"
	template: "templates/controlplane.yaml"
}

cozystack: {
	version: "1.4.1"
	variant: "isp-full"
	publishingHost: "dev.guardianintelligence.org"
	apiServerEndpoint: "https://api.dev.guardianintelligence.org:443"
	exposedServices: ["dashboard", "api"]
	removeControlPlaneTaint: true
}

bootstrap: {
	destructive: true
	requireMaintenance: true
	targetState: "talos-maintenance"
}

hello: {
	enabled: true
	namespace: "guardian-hello"
}
