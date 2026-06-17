package hosts

#Identifier: =~"^[a-z0-9]+(-[a-z0-9]+)*$"
#IPv4:       =~"^([0-9]{1,3}\\.){3}[0-9]{1,3}$"
#Mac:        =~"^[0-9a-f]{2}(:[0-9a-f]{2}){5}$"

#HostConfig: {
	host:        #Identifier
	environment: #Identifier

	provider: {
		name:     #Identifier
		serverId: string
		metro:    #Identifier
		plan:     string
	}

	cluster: {
		name:     #Identifier
		endpoint: =~"^https://.+:6443$"
	}

	node: {
		address:           #IPv4
		hostname:          #Identifier
		prefixLength:      >=1 & <=32
		gateway:           #IPv4
		interfaceMac:      #Mac
		installDiskSerial: string
		zfsDiskSerial:     string
	}

	storage: {
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
}
