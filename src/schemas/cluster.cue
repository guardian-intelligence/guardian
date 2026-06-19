package schemas

#Identifier: =~"^[a-z0-9]+(-[a-z0-9]+)*$"

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
		platformVariant:         *"isp-full" | "isp-full" | "isp-hosted" | "isp-full-generic"
		publishingHost:          *"" | string
		exposedServices:         *([]) | [...("api" | "dashboard" | "cdi-uploadproxy" | "vm-exportproxy")]
		removeControlPlaneTaint: *false | bool
	}

	bootstrap: {
		destructive:        bool
		requireMaintenance: bool
		targetState:        *"stock-ubuntu" | "stock-ubuntu"
		genesis: {
			ageRecipients: [...=~"^age1"]
		}
	}
}
