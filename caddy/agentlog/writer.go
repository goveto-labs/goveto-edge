// Package agentlog registers a Caddy log writer backed by the agent queue.
package agentlog

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/caddyserver/caddy/v2"
)

type Sink interface {
	WriteCaddyLog(siteID string, configVersion uint64, payload []byte) error
}

var state struct {
	sync.RWMutex
	sink Sink
}

func init()             { caddy.RegisterModule(Writer{}) }
func SetSink(sink Sink) { state.Lock(); state.sink = sink; state.Unlock() }

type Writer struct {
	SiteID        string `json:"site_id,omitempty"`
	ConfigVersion uint64 `json:"config_version,omitempty"`
}

func (Writer) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{ID: "caddy.logging.writers.goveto_buffer", New: func() caddy.Module { return new(Writer) }}
}
func (w Writer) String() string {
	if w.SiteID == "" {
		return "Goveto durable agent log buffer"
	}
	return fmt.Sprintf("Goveto durable agent log buffer for %s at version %d", w.SiteID, w.ConfigVersion)
}
func (w Writer) WriterKey() string {
	return fmt.Sprintf("goveto_buffer:%s:%d", w.SiteID, w.ConfigVersion)
}
func (w Writer) OpenWriter() (io.WriteCloser, error) {
	state.RLock()
	sink := state.sink
	state.RUnlock()
	if sink == nil {
		return nil, errors.New("agent log queue is not ready")
	}
	return &queueWriter{sink: sink, siteID: w.SiteID, configVersion: w.ConfigVersion}, nil
}

type queueWriter struct {
	sink          Sink
	siteID        string
	configVersion uint64
}

func (w *queueWriter) Write(data []byte) (int, error) {
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		if err := w.sink.WriteCaddyLog(w.siteID, w.configVersion, append([]byte(nil), line...)); err != nil {
			return 0, fmt.Errorf("buffer caddy log: %w", err)
		}
	}
	return len(data), nil
}
func (*queueWriter) Close() error { return nil }

var _ caddy.WriterOpener = (*Writer)(nil)
