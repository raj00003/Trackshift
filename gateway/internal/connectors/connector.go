package connectors

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/rs/zerolog"
	manifestpb "github.com/trackshift/platform/proto/trackshift/manifest"
)

// Connector copies manifests/chunks to an external target (cloud/object store/etc).
type Connector interface {
	Name() string
	StoreManifest(ctx context.Context, manifest *manifestpb.Manifest, payload []byte) error
	StoreChunk(ctx context.Context, jobID string, chunkIndex uint32, data []byte) error
}

// LoadFromEnv instantiates connectors declared in CONNECTORS env variable.
func LoadFromEnv(ctx context.Context, logger zerolog.Logger) []Connector {
	raw := os.Getenv("CONNECTORS")
	if raw == "" {
		return nil
	}
	var instances []Connector
	for _, token := range strings.Split(raw, ",") {
		token = strings.TrimSpace(strings.ToLower(token))
		if token == "" {
			continue
		}
		var (
			conn Connector
			err  error
		)
		switch token {
		case "s3":
			conn, err = NewS3Connector(ctx)
		case "azure":
			conn, err = NewAzureBlobConnector(ctx)
		case "sftp":
			conn, err = NewSFTPConnector()
		case "ftps":
			conn, err = NewFTPSConnector()
		default:
			err = fmt.Errorf("unknown connector %q", token)
		}
		if err != nil {
			logger.Error().Err(err).Str("connector", token).Msg("failed to init connector")
			continue
		}
		logger.Info().Str("connector", conn.Name()).Msg("initialized connector")
		instances = append(instances, conn)
	}
	return instances
}
