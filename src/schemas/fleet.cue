package schemas

#Identifier: =~"^[a-z0-9]+(-[a-z0-9]+)*$"

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
