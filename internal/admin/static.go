package admin

import "embed"

// staticContent contains the built admin console assets.
//
//go:embed static
var staticContent embed.FS
