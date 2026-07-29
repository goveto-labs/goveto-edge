// Package analyticsschema embeds versioned TimescaleDB migrations.
package analyticsschema

import "embed"

// FS contains analytics migrations compiled into the control API binary.
//
//go:embed migrations/*.sql
var FS embed.FS
