// Package schema embeds the GCORM schema used to initialize and update the database.
package schema

import "embed"

// FS contains every GCORM schema file compiled into the control API binary.
//
//go:embed *.gcorm
var FS embed.FS
