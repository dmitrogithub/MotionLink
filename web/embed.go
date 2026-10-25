// Package web embeds the built browser client (npm run build) so the host and
// the relay can serve it to phones and other PCs without a separate web server.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// Assets returns the built client rooted at its index.html.
func Assets() fs.FS {
	assets, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err)
	}
	return assets
}
