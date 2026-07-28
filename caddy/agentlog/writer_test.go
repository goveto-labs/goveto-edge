package agentlog

import "testing"

type recordingSink struct {
	siteID        string
	configVersion uint64
	payload       []byte
}

func (s *recordingSink) WriteCaddyLog(siteID string, configVersion uint64, payload []byte) error {
	s.siteID, s.configVersion, s.payload = siteID, configVersion, payload
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
	if sink.siteID != "site-a" || sink.configVersion != 42 || len(sink.payload) == 0 {
		t.Fatalf("unexpected sink metadata: %#v", sink)
	}
}
