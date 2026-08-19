// The chunkies gateway process: public WebTransport, admission, and chunk
// routing. Its own binary (not an argv-dispatched twin) so a change to
// runtime code never changes these bytes — image automation rolls only
// the component that updated.
package main

import "github.com/guardian-intelligence/guardian/src/chunkies/gateway"

func main() {
	gateway.Run()
}
