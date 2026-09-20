package waf

import (
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/url"
	"strings"
)

// The caller bounds body before parsing. Parse only that copy, never spool
// multipart files or consume the request stream that the origin will receive.
func decodedBodyCandidates(contentType, body string) []requestCandidate {
	mediaType, parameters, err := mime.ParseMediaType(contentType)
	if err != nil {
		return nil
	}
	var candidates []requestCandidate
	add := func(name, value string) {
		candidates = append(candidates, requestCandidate{name: "BODY:" + name, value: value, sensitive: sensitiveMatchKey(name)})
	}
	switch {
	case mediaType == "application/x-www-form-urlencoded":
		values, _ := url.ParseQuery(body)
		for _, name := range sortedQueryNames(values) {
			add(name, name)
			for _, value := range values[name] {
				add(name, value)
			}
		}
	case mediaType == "application/json" || strings.HasSuffix(mediaType, "+json"):
		// Tokens preserve duplicate object keys and inspect strings even in
		// a truncated PARTIAL prefix; unmarshalling into a map loses both.
		decoder := json.NewDecoder(strings.NewReader(body))
		for {
			token, err := decoder.Token()
			if err != nil {
				break
			}
			if value, ok := token.(string); ok {
				add("json", value)
			}
		}
	case mediaType == "multipart/form-data":
		reader := multipart.NewReader(strings.NewReader(body), parameters["boundary"])
		for {
			part, err := reader.NextPart()
			if err != nil {
				break
			}
			value, _ := io.ReadAll(part)
			add(part.FormName(), string(value))
			add(part.FormName(), part.FormName())
			if fileName := part.FileName(); fileName != "" {
				add(part.FormName(), fileName)
			}
			_ = part.Close()
		}
	}
	return candidates
}
