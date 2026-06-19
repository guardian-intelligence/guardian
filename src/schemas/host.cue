package schemas

#Identifier: =~"^[a-z0-9]+(-[a-z0-9]+)*$"
#IPv4:       =~"^([0-9]{1,3}\\.){3}[0-9]{1,3}$"
#Mac:        =~"^[0-9a-f]{2}(:[0-9a-f]{2}){5}$"

#Host: {
	asset: #Identifier

	provider: {
		name:      "latitude"
		serverID:  =~"^sv_"
		projectID: =~"^proj_"
		site:      =~"^[A-Z0-9]+$"
		plan:      string
	}

	network: {
		ipv4:         #IPv4
		gateway:      #IPv4
		prefixLength: >=1 & <=32
		interfaceMAC: #Mac
	}

	disks: {
		installSerial: string
		dataSerials: [...string]
	}

	storage?: {
		pools: [...{
			name:          #Identifier
			type:          "zfs"
			role:          #Identifier
			deviceSerials: [...string]
			wipePolicy:    "never"
			mountpoint:    =~"^/.+"
		}]
	}

	talos: {
		schematic: =~"^src/hosts/.+/talos/schematic\\.yaml$"
		patches: [...=~"^src/.+\\.yaml$"]
	}

	assignment: {
		cluster:            #Identifier
		environment:        #Identifier
		nodeHostname:       #Identifier
		role:               *"control-plane" | string
		destructiveAllowed: bool
		prod:               bool
	}
}
