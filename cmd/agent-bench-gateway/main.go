package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"log"
	"net"
	"os"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	_ "google.golang.org/grpc/encoding/gzip"
	"goveto-edge/internal/edgeprotocol"
)

type gateway struct {
	ackDelay   time.Duration
	rejectLogs bool
	tasks      []edgeprotocol.AgentTask
}

func (gateway *gateway) Connect(stream edgeprotocol.ManagementConnectServer) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	if first.Hello == nil || first.Hello.NodeID == "" {
		return errors.New("first message must be agent hello")
	}
	if err := stream.Send(&edgeprotocol.ServerMessage{Welcome: &edgeprotocol.ServerWelcome{HeartbeatSeconds: 10, MaxInflightTasks: 16, RotateBeforeHours: 168, MaxLogBatchRecords: 2000, MaxLogBatchBytes: 4 << 20}}); err != nil {
		return err
	}
	for index := range gateway.tasks {
		if err := stream.Send(&edgeprotocol.ServerMessage{Task: &gateway.tasks[index]}); err != nil {
			return err
		}
	}
	for {
		message, err := stream.Recv()
		if errors.Is(err, io.EOF) || errors.Is(err, context.Canceled) {
			return nil
		}
		if err != nil {
			return err
		}
		if message.Logs == nil {
			continue
		}
		if gateway.ackDelay > 0 {
			select {
			case <-stream.Context().Done():
				return stream.Context().Err()
			case <-time.After(gateway.ackDelay):
			}
		}
		ack := &edgeprotocol.AgentLogAck{Through: message.Logs.Through, Accepted: !gateway.rejectLogs}
		if gateway.rejectLogs {
			ack.Error = "benchmark rejection"
			ack.RetryAfterMS = 1000
		}
		if err := stream.Send(&edgeprotocol.ServerMessage{LogsAck: ack}); err != nil {
			return err
		}
	}
}

func main() {
	listenAddress := flag.String("listen", ":8443", "listen address")
	certificatePath := flag.String("cert", "/certs/server.crt", "server certificate")
	keyPath := flag.String("key", "/certs/server.key", "server private key")
	clientCAPath := flag.String("client-ca", "/certs/ca.crt", "trusted client CA")
	ackDelay := flag.Duration("ack-delay", 0, "log ACK delay")
	rejectLogs := flag.Bool("reject-logs", false, "reject log batches")
	taskPath := flag.String("task", "", "optional AgentTask JSON sent after hello")
	flag.Parse()
	pair, err := tls.LoadX509KeyPair(*certificatePath, *keyPath)
	if err != nil {
		log.Fatal(err)
	}
	caPEM, err := os.ReadFile(*clientCAPath)
	if err != nil {
		log.Fatal(err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		log.Fatal("client CA has no certificates")
	}
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{pair}, ClientCAs: roots, ClientAuth: tls.RequireAndVerifyClientCert, NextProtos: []string{"h2"}}
	listener, err := net.Listen("tcp", *listenAddress)
	if err != nil {
		log.Fatal(err)
	}
	var tasks []edgeprotocol.AgentTask
	if *taskPath != "" {
		data, readErr := os.ReadFile(*taskPath)
		if readErr != nil {
			log.Fatal(readErr)
		}
		if len(data) > 0 && data[0] == '[' {
			if decodeErr := json.Unmarshal(data, &tasks); decodeErr != nil {
				log.Fatal(decodeErr)
			}
		} else {
			var task edgeprotocol.AgentTask
			if decodeErr := json.Unmarshal(data, &task); decodeErr != nil {
				log.Fatal(decodeErr)
			}
			tasks = append(tasks, task)
		}
	}
	server := grpc.NewServer(grpc.Creds(credentials.NewTLS(tlsConfig)), grpc.ForceServerCodec(edgeprotocol.JSONCodec{}))
	edgeprotocol.RegisterManagementServer(server, &gateway{ackDelay: *ackDelay, rejectLogs: *rejectLogs, tasks: tasks})
	log.Printf("mock mTLS gateway listening on %s", *listenAddress)
	log.Fatal(server.Serve(listener))
}
