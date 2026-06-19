package schemas

#Cluster: {
	cluster: {
		name: string
		endpoint: =~"^https://"
		domain: string
		apiServerDomain?: string
		podCIDR: *"10.244.0.0/16" | string
		serviceCIDR: *"10.96.0.0/16" | string
		joinCIDR: *"100.64.0.0/16" | string
		advertisedCIDR: string
	}
	node: {
		name: string
		address: string
		hostname: string
		interfaceMac: string
		installDiskSerial: string
		role: *"control-plane" | string
	}
	talm: {
		preset: *"cozystack" | "cozystack"
		talosVersion: string
		kubernetesVersion: string
		installerImage: string
		template: *"templates/controlplane.yaml" | string
	}
	cozystack: {
		version: string
		variant: *"isp-full" | string
		publishingHost: string
		apiServerEndpoint: =~"^https://"
		exposedServices: [...string]
		removeControlPlaneTaint: *false | bool
	}
	bootstrap: {
		destructive: bool
		requireMaintenance: bool
		targetState: *"talos-maintenance" | "talos-maintenance"
		genesis?: {
			ageRecipients: [...=~"^age1"]
		}
	}
	hello: {
		enabled: *true | bool
		namespace: *"guardian-hello" | string
	}
}

#GuardianCluster: {
	name:            #Identifier
	domain:          string
	apiServerDomain: string

	members: [...#Identifier]
	environments: [...#Identifier]

	network: {
		podCIDR:        *"10.244.0.0/16" | string
		serviceCIDR:    *"10.96.0.0/16" | string
		joinCIDR:       *"100.64.0.0/16" | string
		advertisedCIDR?: string
	}

	talos: {
		version:           =~"^v"
		talmVersion:       =~"^v"
		kubernetesVersion: string
		installerImage:    string
	}

	cozystack: {
		version:                 string
		variant:                 *"isp-full" | string
		removeControlPlaneTaint: *false | bool
	}

	bootstrap: {
		destructive:        bool
		requireMaintenance: bool
		targetState:        *"talos-maintenance" | "talos-maintenance"
		genesis: {
			ageRecipients: [...=~"^age1"]
		}
	}
}
