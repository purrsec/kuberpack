package provisioning

import "embed"

// Files ships the provisioning charts with the CLI; installation does not
// require a checkout.
//
//go:embed all:charts/kuberpack all:charts/stateless
var Files embed.FS
