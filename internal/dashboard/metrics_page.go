package dashboard

import _ "embed" // see index.go

//go:embed assets/metrics.html
var metricsHTML string

//go:embed assets/metrics.js
var metricsScript string
