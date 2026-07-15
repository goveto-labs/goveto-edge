// Package clickhouseschema embeds the ClickHouse schema used by the control API.
package clickhouseschema

import "embed"

// FS contains the ClickHouse schema compiled into the control API binary.
//
//go:embed schema.sql
var FS embed.FS
