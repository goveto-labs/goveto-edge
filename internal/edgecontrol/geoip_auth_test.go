package edgecontrol

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	"goveto-edge/internal/edgeprotocol"
)

func TestDownloadGeoIPRejectsCertificateNodeMismatch(t *testing.T) {
	certificate := &x509.Certificate{Subject: pkix.Name{CommonName: "node-a"}}
	ctx := peer.NewContext(context.Background(), &peer.Peer{AuthInfo: credentials.TLSInfo{
		State: tls.ConnectionState{PeerCertificates: []*x509.Certificate{certificate}},
	}})
	err := (&Gateway{}).DownloadGeoIP(&edgeprotocol.GeoIPDownloadRequest{NodeID: "node-b", SHA256: "version"}, &fakeGeoIPDownloadServer{ctx: ctx})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("certificate mismatch returned %v", err)
	}
}

type fakeGeoIPDownloadServer struct{ ctx context.Context }

func (f *fakeGeoIPDownloadServer) Send(*edgeprotocol.GeoIPChunk) error { return nil }
func (f *fakeGeoIPDownloadServer) SetHeader(metadata.MD) error         { return nil }
func (f *fakeGeoIPDownloadServer) SendHeader(metadata.MD) error        { return nil }
func (f *fakeGeoIPDownloadServer) SetTrailer(metadata.MD)              {}
func (f *fakeGeoIPDownloadServer) Context() context.Context            { return f.ctx }
func (f *fakeGeoIPDownloadServer) SendMsg(any) error                   { return nil }
func (f *fakeGeoIPDownloadServer) RecvMsg(any) error                   { return nil }

var _ grpc.ServerStream = (*fakeGeoIPDownloadServer)(nil)
