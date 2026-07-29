package agentlog

import (
	"testing"
	"time"
)

type recordingSink struct {
	siteID        string
	configVersion uint64
	payload       []byte
	receivedAt    time.Time
}

func (s *recordingSink) WriteCaddyLog(siteID string, configVersion uint64, receivedAt time.Time, payload []byte) error {
	s.siteID, s.configVersion, s.receivedAt, s.payload = siteID, configVersion, receivedAt, payload
	return nil
}

func TestWriterKeepsSiteMetadataIsolated(t *testing.T) {
	if (Writer{SiteID: "site-a", ConfigVersion: 1}).WriterKey() ==
		(Writer{SiteID: "site-b", ConfigVersion: 1}).WriterKey() {
		t.Fatal("site writers share a cache key")
	}
	sink := &recordingSink{}
	SetSink(sink)
	t.Cleanup(func() { SetSink(nil) })
	opener := Writer{SiteID: "site-a", ConfigVersion: 42}
	writer, err := opener.OpenWriter()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte("{\"status\":200}\n")); err != nil {
		t.Fatal(err)
	}
	if sink.siteID != "site-a" || sink.configVersion != 42 || sink.receivedAt.IsZero() || len(sink.payload) == 0 {
		t.Fatalf("unexpected sink metadata: %#v", sink)
	}
}
