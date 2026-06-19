package schemas

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
