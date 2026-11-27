// Package ui holds the web UI of gotunables: one HTML file, no build step. It is a package of its own
// so that it can live in a repository of its own later; the handler only serves HTML.
package ui

import _ "embed"

// HTML is the page. The server fills in the name, the description and the current values (see the markers in
// handler.go); the page works without them too and then loads everything itself.
//
//go:embed index.html
var HTML []byte
