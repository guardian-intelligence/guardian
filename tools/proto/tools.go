//go:build tools

// Pins the proto codegen plugin versions in go.mod (gazelle's go.mod parser
// predates the `tool` directive, so the classic blank-import pattern keeps
// generation reproducible instead). Regeneration recipe: buf.gen.yaml.
package tools

import (
	_ "connectrpc.com/connect/cmd/protoc-gen-connect-go"
	_ "google.golang.org/protobuf/cmd/protoc-gen-go"
)
