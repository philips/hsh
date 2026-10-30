//go:build gvisor_embed

package gvisor

import _ "embed"

//go:embed payload.tar.gz
var payload []byte

//go:embed payload.version
var payloadVersion string

//go:embed payload.sha256
var payloadSHA256 string
