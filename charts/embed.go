package charts

import "embed"

// Files ships the charts with the CLI; installation does not require a checkout.
//
//go:embed all:kuberpack all:stateless
var Files embed.FS
