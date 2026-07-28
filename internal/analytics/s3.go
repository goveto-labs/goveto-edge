package analytics

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsv4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
)

type S3Options struct {
	Endpoint     string
	Bucket       string
	Region       string
	AccessKey    string
	SecretKey    string
	SessionToken string
	HTTPClient   *http.Client
}

type S3ObjectStore struct {
	endpoint    *url.URL
	bucket      string
	region      string
	credentials aws.Credentials
	signer      *awsv4.Signer
	client      *http.Client
	now         func() time.Time
}

func NewS3ObjectStore(options S3Options) (*S3ObjectStore, error) {
	endpoint, err := url.Parse(strings.TrimSpace(options.Endpoint))
	if err != nil || endpoint.Scheme != "http" && endpoint.Scheme != "https" || endpoint.Host == "" {
		return nil, errors.New("S3 archive endpoint must be an HTTP(S) URL")
	}
	if options.Bucket == "" || options.Region == "" || options.AccessKey == "" || options.SecretKey == "" {
		return nil, errors.New("S3 archive bucket, region, access key, and secret key are required")
	}
	client := options.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return &S3ObjectStore{
		endpoint: endpoint, bucket: options.Bucket, region: options.Region,
		credentials: aws.Credentials{
			AccessKeyID: options.AccessKey, SecretAccessKey: options.SecretKey,
			SessionToken: options.SessionToken, Source: "goveto static configuration",
		},
		signer: awsv4.NewSigner(), client: client, now: time.Now,
	}, nil
}

func (s *S3ObjectStore) Put(ctx context.Context, key string, object ArchiveObject) error {
	payloadHash := sha256Hex(object.Data)
	timestamp := s.now().UTC()
	target := *s.endpoint
	target.Path = path.Join(target.Path, s.bucket, key)

	request, err := http.NewRequestWithContext(ctx, http.MethodPut, target.String(), bytes.NewReader(object.Data))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", object.ContentType)
	if object.ContentEncoding != "" {
		request.Header.Set("Content-Encoding", object.ContentEncoding)
	}
	request.Header.Set("X-Amz-Content-Sha256", payloadHash)
	if err := s.signer.SignHTTP(
		ctx, s.credentials, request, payloadHash, "s3", s.region, timestamp,
	); err != nil {
		return fmt.Errorf("sign S3 archive PUT: %w", err)
	}

	response, err := s.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return fmt.Errorf("S3 archive PUT returned %s: %s", response.Status, strings.TrimSpace(string(body)))
	}
	return nil
}

func sha256Hex(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}
