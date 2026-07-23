package policy

import (
	"errors"
	"fmt"
	"mime"
	"regexp"
	"sort"
	"strings"
)

const defaultCompressionMaxLength int64 = 10 << 20
const maximumCompressionLength int64 = 64 << 20

type CompressionPolicy struct {
	Enabled            bool     `json:"enabled"`
	Extensions         []string `json:"extensions"`
	ExcludedExtensions []string `json:"excluded_extensions"`
	MIMETypes          []string `json:"mime_types"`
	Recompress         bool     `json:"recompress"`
	MinimumLength      int64    `json:"minimum_length"`
	MaximumLength      int64    `json:"maximum_length"`
	ExcludedPaths      []string `json:"excluded_paths"`
}

func DefaultCompressionPolicy() CompressionPolicy {
	return CompressionPolicy{
		Extensions: []string{
			"css", "csv", "htm", "html", "js", "json", "map", "md", "mjs", "svg", "txt", "wasm", "xml",
		},
		ExcludedExtensions: []string{
			"7z", "avif", "br", "bz2", "gif", "gz", "jpeg", "jpg", "mov", "mp3", "mp4", "pdf", "png", "rar", "webm", "webp", "zip", "zst",
		},
		MIMETypes: []string{
			"application/javascript",
			"application/json",
			"application/manifest+json",
			"application/wasm",
			"application/xml",
			"image/svg+xml",
			"text/*",
		},
		MinimumLength: 1024,
		MaximumLength: defaultCompressionMaxLength,
		ExcludedPaths: []string{},
	}
}

func (p *CompressionPolicy) NormalizeAndValidate() error {
	if p.MinimumLength == 0 {
		p.MinimumLength = 1024
	}
	if p.MaximumLength == 0 {
		p.MaximumLength = defaultCompressionMaxLength
	}
	if p.MinimumLength < 0 {
		return errors.New("minimum_length cannot be negative")
	}
	if p.MaximumLength < 1 || p.MaximumLength > maximumCompressionLength {
		return errors.New("maximum_length must be between 1 and 67108864")
	}
	if p.MinimumLength > p.MaximumLength {
		return errors.New("minimum_length cannot exceed maximum_length")
	}

	var err error
	p.Extensions, err = normalizeCompressionExtensions(p.Extensions, "extensions")
	if err != nil {
		return err
	}
	p.ExcludedExtensions, err = normalizeCompressionExtensions(p.ExcludedExtensions, "excluded_extensions")
	if err != nil {
		return err
	}

	seenMIME := make(map[string]struct{}, len(p.MIMETypes))
	for index, value := range p.MIMETypes {
		value = strings.ToLower(strings.TrimSpace(value))
		if strings.HasSuffix(value, "/*") {
			if _, _, parseErr := mime.ParseMediaType(strings.TrimSuffix(value, "*") + "plain"); parseErr != nil {
				return fmt.Errorf("invalid MIME type %q", value)
			}
		} else if mediaType, _, parseErr := mime.ParseMediaType(value); parseErr != nil || mediaType != value {
			return fmt.Errorf("invalid MIME type %q", value)
		}
		if _, exists := seenMIME[value]; exists {
			return fmt.Errorf("duplicate MIME type %q", value)
		}
		seenMIME[value] = struct{}{}
		p.MIMETypes[index] = value
	}
	sort.Strings(p.MIMETypes)
	if p.MIMETypes == nil {
		p.MIMETypes = []string{}
	}

	seenPaths := make(map[string]struct{}, len(p.ExcludedPaths))
	for index, value := range p.ExcludedPaths {
		value = strings.TrimSpace(value)
		if value == "" || !strings.HasPrefix(value, "/") {
			return fmt.Errorf("excluded path %q must start with /", value)
		}
		if _, exists := seenPaths[value]; exists {
			return fmt.Errorf("duplicate excluded path %q", value)
		}
		seenPaths[value] = struct{}{}
		p.ExcludedPaths[index] = value
	}
	sort.Strings(p.ExcludedPaths)
	if p.ExcludedPaths == nil {
		p.ExcludedPaths = []string{}
	}

	if p.Enabled && len(p.Extensions) == 0 && len(p.MIMETypes) == 0 {
		return errors.New("enabled compression requires at least one extension or MIME type")
	}
	return nil
}

func normalizeCompressionExtensions(values []string, field string) ([]string, error) {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, len(values))
	for index, value := range values {
		value = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(value)), ".")
		if !regexp.MustCompile(`^[a-z0-9][a-z0-9_+-]*$`).MatchString(value) {
			return nil, fmt.Errorf("invalid %s value %q", field, value)
		}
		if _, exists := seen[value]; exists {
			return nil, fmt.Errorf("duplicate %s value %q", field, value)
		}
		seen[value] = struct{}{}
		result[index] = value
	}
	sort.Strings(result)
	if result == nil {
		result = []string{}
	}
	return result, nil
}
