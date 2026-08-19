// The chunkie process: the chunkies server runtime hosting this pod's
// chunk authorities. Its own binary (not an argv-dispatched twin) so a
// change to gateway code never changes these bytes — image automation
// rolls only the component that updated.
package main

import "github.com/guardian-intelligence/guardian/src/chunkies/chunkie"

func main() {
	chunkie.Run()
}
