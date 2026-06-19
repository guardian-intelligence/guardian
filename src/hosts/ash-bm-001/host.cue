package host

asset: "ash-bm-001"

provider: {
	name: "latitude"
	serverID: "sv_vAPXaMxKM5epz"
	projectID: "proj_ZWr75Zdbm0A91"
	site: "ASH"
	plan: "f4-metal-small"
}

network: {
	ipv4: "206.223.228.101"
	gateway: "206.223.228.100"
	prefixLength: 31
	interfaceMAC: "90:5a:08:33:ba:9f"
}

disks: {
	installSerial: "362510FCEFB8"
	dataSerials: ["362510FD7C47"]
}

storage: {
	pools: [{
		name: "guardian"
		type: "zfs"
		role: "product-workloads"
		deviceSerials: ["362510FD7C47"]
		wipePolicy: "never"
		mountpoint: "/var/mnt/guardian"
	}]
}

talos: {
	schematic: "src/hosts/ash-bm-001/talos/schematic.yaml"
	patches: [
		"src/hosts/ash-bm-001/talos/patches/single-node.yaml",
		"src/hosts/ash-bm-001/talos/patches/registry-mirror.yaml",
		"src/hosts/ash-bm-001/talos/patches/image-cache.yaml",
		"src/hosts/ash-bm-001/talos/patches/zfs-module.yaml",
		"src/hosts/ash-bm-001/talos/patches/cni-none.yaml",
		"src/hosts/ash-bm-001/talos/patches/ingress-firewall.yaml",
	]
}

assignment: {
	cluster: "guardian-nonprod"
	environment: "dev"
	nodeHostname: "gi-ash-bm-001"
	role: "control-plane"
	destructiveAllowed: true
	prod: false
}
