package storage

import (
	"context"
	"fmt"
	"io/fs"
	"strings"

	"github.com/ClickHouse/clickhouse-go/v2"
)

type clickhouseSchemaExecutor interface {
	Exec(context.Context, string, ...any) error
}

// InitClickHouseSchema applies every statement in schema.sql. Statements in
// the embedded schema must be idempotent so this can run on every startup.
func InitClickHouseSchema(ctx context.Context, conn clickhouseSchemaExecutor, schemaFS fs.FS) (int, error) {
	raw, err := fs.ReadFile(schemaFS, "schema.sql")
	if err != nil {
		return 0, fmt.Errorf("read ClickHouse schema: %w", err)
	}

	statements, err := splitClickHouseStatements(string(raw))
	if err != nil {
		return 0, fmt.Errorf("parse ClickHouse schema: %w", err)
	}
	for i, statement := range statements {
		if err := conn.Exec(ctx, statement); err != nil {
			return i, fmt.Errorf("apply ClickHouse schema statement %d: %w", i+1, err)
		}
	}
	return len(statements), nil
}

func splitClickHouseStatements(sql string) ([]string, error) {
	var statements []string
	start := 0
	quote := byte(0)
	lineComment := false
	blockComment := false

	for i := 0; i < len(sql); i++ {
		c := sql[i]
		if lineComment {
			if c == '\n' {
				lineComment = false
			}
			continue
		}
		if blockComment {
			if c == '*' && i+1 < len(sql) && sql[i+1] == '/' {
				blockComment = false
				i++
			}
			continue
		}
		if quote != 0 {
			if c == '\\' {
				i++
				continue
			}
			if c == quote {
				if i+1 < len(sql) && sql[i+1] == quote {
					i++
					continue
				}
				quote = 0
			}
			continue
		}

		switch {
		case c == '-' && i+1 < len(sql) && sql[i+1] == '-':
			lineComment = true
			i++
		case c == '/' && i+1 < len(sql) && sql[i+1] == '*':
			blockComment = true
			i++
		case c == '\'' || c == '"' || c == '`':
			quote = c
		case c == ';':
			if statement := strings.TrimSpace(sql[start:i]); statement != "" {
				statements = append(statements, statement)
			}
			start = i + 1
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("unterminated quoted string")
	}
	if blockComment {
		return nil, fmt.Errorf("unterminated block comment")
	}
	if statement := strings.TrimSpace(sql[start:]); statement != "" {
		statements = append(statements, statement)
	}
	return statements, nil
}

func OpenClickHouse(ctx context.Context, dsn string) (clickhouse.Conn, error) {
	options, err := clickhouse.ParseDSN(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse ClickHouse DSN: %w", err)
	}
	connection, err := clickhouse.Open(options)
	if err != nil {
		return nil, fmt.Errorf("open ClickHouse: %w", err)
	}
	if err := connection.Ping(ctx); err != nil {
		connection.Close()
		return nil, fmt.Errorf("ping ClickHouse: %w", err)
	}
	return connection, nil
}
