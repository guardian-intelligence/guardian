package schemas

#Identifier: =~"^[a-z0-9]+(-[a-z0-9]+)*$"

#Environment: {
	name:      #Identifier
	cluster:   #Identifier
	namespace: #Identifier

	crossplane: {
		environmentConfig: #Identifier
	}

	domains: {
		company?: string
		aisucks?: string
		oci?:     string
	}
}
