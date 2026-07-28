package edgeprotocol_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/test/bufconn"

	"goveto-edge/internal/edgecontrol"
	"goveto-edge/internal/edgeprotocol"
	"goveto-edge/internal/node"
)

func TestManagementStreamOverMutualTLS(t *testing.T) {
	cipher, err := node.NewCredentialCipher(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	authority, err := edgecontrol.NewAuthority(cipher, "control.example:8443")
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := authority.IssueNode("550e8400-e29b-41d4-a716-446655440000")
	if err != nil {
		t.Fatal(err)
	}

	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer(
		grpc.Creds(credentials.NewTLS(authority.ServerTLSConfig())),
		grpc.ForceServerCodec(edgeprotocol.JSONCodec{}),
	)
	edgeprotocol.RegisterManagementServer(server, welcomeServer{})
	go func() { _ = server.Serve(listener) }()
	defer server.Stop()

	pair, err := tls.X509KeyPair([]byte(bundle.Certificate), []byte(bundle.PrivateKey))
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM([]byte(bundle.CACertificate))
	connection, err := grpc.NewClient(
		"passthrough:///control.example:8443",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }),
		grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{
			MinVersion: tls.VersionTLS13, ServerName: bundle.ServerName,
			RootCAs: roots, Certificates: []tls.Certificate{pair}, NextProtos: []string{"h2"},
		})),
		grpc.WithDefaultCallOptions(grpc.ForceCodec(edgeprotocol.JSONCodec{})),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream, err := edgeprotocol.NewManagementClient(connection).Connect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(&edgeprotocol.ClientMessage{Hello: &edgeprotocol.AgentHello{NodeID: bundle.NodeID}}); err != nil {
		t.Fatal(err)
	}
	message, err := stream.Recv()
	if err != nil {
		t.Fatal(err)
	}
	if message.Welcome == nil || message.Welcome.HeartbeatSeconds != 10 {
		t.Fatalf("unexpected welcome frame: %#v", message)
	}
	download, err := edgeprotocol.NewManagementClient(connection).DownloadGeoIP(ctx, &edgeprotocol.GeoIPDownloadRequest{NodeID: bundle.NodeID, SHA256: "version"})
	if err != nil {
		t.Fatal(err)
	}
	chunk, err := download.Recv()
	if err != nil {
		t.Fatal(err)
	}
	if chunk.Offset != 0 || string(chunk.Data) != "database" {
		t.Fatalf("unexpected GeoIP chunk: %#v", chunk)
	}
}

type welcomeServer struct{}

func (welcomeServer) Connect(stream edgeprotocol.ManagementConnectServer) error {
	message, err := stream.Recv()
	if err != nil {
		return err
	}
	if message.Hello == nil {
		return nil
	}
	return stream.Send(&edgeprotocol.ServerMessage{Welcome: &edgeprotocol.ServerWelcome{HeartbeatSeconds: 10}})
}

func (welcomeServer) DownloadGeoIP(request *edgeprotocol.GeoIPDownloadRequest, stream edgeprotocol.ManagementDownloadGeoIPServer) error {
	if request.NodeID == "" || request.SHA256 != "version" {
		return errors.New("invalid download request")
	}
	return stream.Send(&edgeprotocol.GeoIPChunk{Offset: 0, Data: []byte("database")})
}
