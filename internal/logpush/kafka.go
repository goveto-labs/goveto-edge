package logpush

import (
	"context"
	"crypto/tls"
	"fmt"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/kmsg"
	"github.com/twmb/franz-go/pkg/sasl"
	"github.com/twmb/franz-go/pkg/sasl/plain"
	"github.com/twmb/franz-go/pkg/sasl/scram"
)

// producer abstracts the subset of *kgo.Client the dispatcher relies on so
// tests can substitute a fake.
type producer interface {
	Produce(ctx context.Context, record *kgo.Record, onDone func(*kgo.Record, error))
	Close()
}

// kafkaOptions builds the franz-go client options shared by the dispatcher
// producer and the connectivity test client.
func kafkaOptions(dest Destination) ([]kgo.Opt, error) {
	opts := []kgo.Opt{
		kgo.SeedBrokers(dest.Brokers...),
		kgo.ClientID("goveto-edge-logpush"),
	}
	if dest.TLSEnabled {
		opts = append(opts, kgo.DialTLSConfig(&tls.Config{MinVersion: tls.VersionTLS12}))
	}
	mechanism, err := saslMechanism(dest)
	if err != nil {
		return nil, err
	}
	if mechanism != nil {
		opts = append(opts, kgo.SASL(mechanism))
	}
	return opts, nil
}

func saslMechanism(dest Destination) (sasl.Mechanism, error) {
	switch dest.SASLMechanism {
	case SASLNone:
		return nil, nil
	case SASLPlain:
		return plain.Auth{User: dest.Username, Pass: dest.Password}.AsMechanism(), nil
	case SASLSCRAMSHA256:
		return scram.Auth{User: dest.Username, Pass: dest.Password}.AsSha256Mechanism(), nil
	case SASLSCRAMSHA512:
		return scram.Auth{User: dest.Username, Pass: dest.Password}.AsSha512Mechanism(), nil
	default:
		return nil, fmt.Errorf("unsupported SASL mechanism %q", dest.SASLMechanism)
	}
}

// newKafkaProducer builds the long-lived produce client for a destination.
// franz-go batches internally according to linger; RecordRetries bounds
// per-record retries so a down broker fails records instead of buffering
// forever (best-effort semantics).
func newKafkaProducer(dest Destination, linger time.Duration, bufferedRecords, bufferedBytes int) (producer, error) {
	opts, err := kafkaOptions(dest)
	if err != nil {
		return nil, err
	}
	opts = append(opts,
		kgo.DefaultProduceTopic(dest.Topic),
		kgo.ProducerLinger(linger),
		kgo.MaxBufferedRecords(bufferedRecords),
		kgo.MaxBufferedBytes(bufferedBytes),
		kgo.RecordRetries(5),
		kgo.RequiredAcks(kgo.LeaderAck()),
		kgo.ProducerBatchCompression(kgo.SnappyCompression()),
	)
	return kgo.NewClient(opts...)
}

// TestConnection dials the destination and issues a metadata request to prove
// the brokers are reachable and the credentials authenticate.
func TestConnection(ctx context.Context, dest Destination) error {
	opts, err := kafkaOptions(dest)
	if err != nil {
		return err
	}
	opts = append(opts,
		kgo.DialTimeout(10*time.Second),
		kgo.RequestRetries(1),
	)
	client, err := kgo.NewClient(opts...)
	if err != nil {
		return fmt.Errorf("build kafka client: %w", err)
	}
	defer client.Close()
	request := kmsg.NewMetadataRequest()
	if _, err = request.RequestWith(ctx, client); err != nil {
		return fmt.Errorf("metadata request: %w", err)
	}
	return nil
}
