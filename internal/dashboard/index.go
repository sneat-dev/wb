package dashboard

import _ "embed" // the pages and their scripts are files of this package

// The two pages of the dashboard and the script of each. A page has no inline
// script: its script is a file of the daemon's own origin, served under
// AssetsPrefix, so the policy of every dashboard response can refuse inline
// script altogether (see securityHeaders). The scripts build the page with
// createElement and textContent and never concatenate data into HTML.

//go:embed assets/index.html
var indexHTML string

//go:embed assets/index.js
var indexScript string
