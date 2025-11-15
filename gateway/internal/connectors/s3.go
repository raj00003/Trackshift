package connectors

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	manifestpb "github.com/trackshift/platform/proto/trackshift/manifest"
)

type s3Connector struct {
	client *s3.Client
	bucket string
	prefix string
}

func NewS3Connector(ctx context.Context) (Connector, error) {
	bucket := os.Getenv("S3_BUCKET")
	if bucket == "" {
		return nil, fmt.Errorf("S3_BUCKET required when enabling s3 connector")
	}
	prefix := os.Getenv("S3_PREFIX")
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("load aws config: %w", err)
	}
	client := s3.NewFromConfig(cfg)
	return &s3Connector{
		client: client,
		bucket: bucket,
		prefix: prefix,
	}, nil
}

func (s *s3Connector) Name() string {
	return "s3"
}

func (s *s3Connector) StoreManifest(ctx context.Context, manifest *manifestpb.Manifest, payload []byte) error {
	key := s.keyFor(manifest.GetJobId(), "manifest.json")
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
		Body:   bytes.NewReader(payload),
		ACL:    types.ObjectCannedACLPrivate,
		Metadata: map[string]string{
			"file_name": manifest.GetFileName(),
			"job_id":    manifest.GetJobId(),
		},
		ContentType: aws.String("application/json"),
	})
	return err
}

func (s *s3Connector) StoreChunk(ctx context.Context, jobID string, chunkIndex uint32, data []byte) error {
	key := s.keyFor(jobID, fmt.Sprintf("chunks/chunk-%05d.bin", chunkIndex))
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(s.bucket),
		Key:         aws.String(key),
		Body:        bytes.NewReader(data),
		ACL:         types.ObjectCannedACLPrivate,
		ContentType: aws.String("application/octet-stream"),
	})
	return err
}

func (s *s3Connector) keyFor(jobID, suffix string) string {
	if s.prefix == "" {
		return path.Join(jobID, suffix)
	}
	return path.Join(s.prefix, jobID, suffix)
}
