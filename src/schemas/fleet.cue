package schemas

#FleetHost: {
	asset:           #Identifier
	provider:        "latitude"
	serverID:        =~"^sv_"
	cluster:         #Identifier
	environment?:    #Identifier
	prod:            bool
	excluded:        *false | bool
	allowReinstall:  bool
	preventDestroy:  *true | bool
}
