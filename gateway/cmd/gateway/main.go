package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/trackshift/platform/gateway/internal/connectors"
	"github.com/trackshift/platform/gateway/internal/dlp"
	commonpb "github.com/trackshift/platform/proto/trackshift/common"
	manifestpb "github.com/trackshift/platform/proto/trackshift/manifest"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/reflection"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	transferStatusManifest = "MANIFEST_RECEIVED"
	transferStatusChunks   = "CHUNKS_RECEIVED"
)

func main() {
	zerolog.TimeFieldFormat = time.RFC3339
	if err := run(); err != nil {
		log.Fatal().Err(err).Msg("gateway exited")
	}
}

func run() error {
	ctx := context.Background()
	addr := env("GATEWAY_BIND", ":50051")
	jobsDir := env("DATA_DIR", "data/jobs")
	dsn := env("POSTGRES_DSN", "postgres://trackshift:trackshift@localhost:5432/trackshift?sslmode=disable")
	connectorStrict := boolEnv("CONNECTOR_STRICT", true)

	if err := os.MkdirAll(jobsDir, 0o755); err != nil {
		return fmt.Errorf("create jobs dir: %w", err)
	}

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return fmt.Errorf("connect to postgres: %w", err)
	}
	if err := migrate(ctx, pool); err != nil {
		return fmt.Errorf("run migrations: %w", err)
	}

	scanner := dlp.NewRuleScannerFromEnv()
	connectorSet := connectors.LoadFromEnv(ctx, log.Logger)
	if len(connectorSet) > 0 {
		log.Info().Int("count", len(connectorSet)).Msg("external connectors enabled")
	}

	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", addr, err)
	}

	server := &ingestServer{
		jobsDir: jobsDir,
		db:      pool,
		json: protojson.MarshalOptions{
			Multiline: true,
			Indent:    "  ",
		},
		scanner:         scanner,
		connectors:      connectorSet,
		connectorStrict: connectorStrict,
		logger:          log.Logger,
	}

	grpcServer := grpc.NewServer()
	manifestpb.RegisterManifestIngestServiceServer(grpcServer, server)
	reflection.Register(grpcServer)

	log.Info().Str("addr", addr).Msg("gateway gRPC listening")
	return grpcServer.Serve(lis)
}

type ingestServer struct {
	manifestpb.UnimplementedManifestIngestServiceServer
	jobsDir         string
	db              *pgxpool.Pool
	json            protojson.MarshalOptions
	scanner         dlp.Scanner
	connectors      []connectors.Connector
	connectorStrict bool
	logger          zerolog.Logger
}

func (s *ingestServer) UploadManifest(ctx context.Context, req *manifestpb.UploadManifestRequest) (*manifestpb.UploadManifestResponse, error) {
	manifest := req.GetManifest()
	if manifest == nil {
		return nil, status.Error(codes.InvalidArgument, "manifest is required")
	}
	if manifest.GetJobId() == "" {
		return nil, status.Error(codes.InvalidArgument, "jobId missing")
	}

	if err := s.scanManifest(ctx, manifest); err != nil {
		return nil, err
	}

	payload, err := s.saveManifest(manifest)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "persist manifest: %v", err)
	}
	if err := s.fanoutManifest(ctx, manifest, payload); err != nil {
		return nil, status.Errorf(codes.Internal, "replicate manifest: %v", err)
	}
	if err := s.upsertTransfer(ctx, manifest.GetJobId(), transferStatusManifest); err != nil {
		return nil, status.Errorf(codes.Internal, "upsert transfer: %v", err)
	}

	receipt := &commonpb.Receipt{
		ReceiptId:       uuid.NewString(),
		JobId:           manifest.GetJobId(),
		IntegrityStatus: commonpb.IntegrityStatus_INTEGRITY_STATUS_PENDING,
		IssuedAt:        timestamppb.Now(),
	}

	log.Info().Str("job_id", manifest.GetJobId()).Msg("manifest stored")
	return &manifestpb.UploadManifestResponse{Receipt: receipt}, nil
}

func (s *ingestServer) UploadChunk(stream manifestpb.ManifestIngestService_UploadChunkServer) error {
	var jobID string
	var chunkCount int

	for {
		req, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return status.Errorf(codes.Internal, "receive chunk: %v", err)
		}
		if req.GetJobId() == "" {
			return status.Error(codes.InvalidArgument, "jobId missing in chunk")
		}
		if jobID == "" {
			jobID = req.GetJobId()
		}
		if jobID != req.GetJobId() {
			return status.Error(codes.InvalidArgument, "mixed job ids in stream")
		}
		if err := s.scanChunk(stream.Context(), req); err != nil {
			return err
		}
		if err := s.saveChunk(req); err != nil {
			return status.Errorf(codes.Internal, "write chunk: %v", err)
		}
		if err := s.fanoutChunk(stream.Context(), req); err != nil {
			return err
		}
		chunkCount++
	}

	if jobID == "" {
		return status.Error(codes.InvalidArgument, "no chunks received")
	}
	if err := s.upsertTransfer(stream.Context(), jobID, transferStatusChunks); err != nil {
		return status.Errorf(codes.Internal, "update status: %v", err)
	}

	log.Info().Str("job_id", jobID).Int("chunks", chunkCount).Msg("chunks persisted")
	return stream.SendAndClose(&manifestpb.UploadChunkResponse{Accepted: true})
}

func (s *ingestServer) saveManifest(manifest *manifestpb.Manifest) ([]byte, error) {
	jobDir := filepath.Join(s.jobsDir, manifest.GetJobId())
	if err := os.MkdirAll(jobDir, 0o755); err != nil {
		return nil, err
	}
	payload, err := s.json.Marshal(manifest)
	if err != nil {
		return nil, err
	}
	tmp := filepath.Join(jobDir, "manifest.json.tmp")
	if err := os.WriteFile(tmp, payload, 0o644); err != nil {
		return nil, err
	}
	if err := os.Rename(tmp, filepath.Join(jobDir, "manifest.json")); err != nil {
		return nil, err
	}
	return payload, nil
}

func (s *ingestServer) saveChunk(req *manifestpb.UploadChunkRequest) error {
	jobDir := filepath.Join(s.jobsDir, req.GetJobId(), "chunks")
	if err := os.MkdirAll(jobDir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(jobDir, fmt.Sprintf("chunk-%05d.bin", req.GetChunkIndex()))
	return os.WriteFile(path, req.GetData(), 0o644)
}

func (s *ingestServer) upsertTransfer(ctx context.Context, jobID, status string) error {
	const query = `
        INSERT INTO transfers (id, job_id, status, created_at, updated_at)
        VALUES ($1, $2, $3, NOW(), NOW())
        ON CONFLICT (job_id)
        DO UPDATE SET status = EXCLUDED.status, updated_at = NOW();`
	_, err := s.db.Exec(ctx, query, uuid.New(), jobID, status)
	return err
}

func (s *ingestServer) scanManifest(ctx context.Context, manifest *manifestpb.Manifest) error {
	if s.scanner == nil {
		return nil
	}
	if err := s.scanner.ScanManifest(ctx, manifest); err != nil {
		var violation *dlp.Violation
		if errors.As(err, &violation) {
			s.logger.Warn().
				Str("job_id", manifest.GetJobId()).
				Str("rule", violation.Rule).
				Msg("dlp violation on manifest")
			if s.scanner.Enforced() {
				return status.Error(codes.PermissionDenied, violation.Error())
			}
			return nil
		}
		return status.Errorf(codes.Internal, "manifest scan failed: %v", err)
	}
	return nil
}

func (s *ingestServer) scanChunk(ctx context.Context, req *manifestpb.UploadChunkRequest) error {
	if s.scanner == nil {
		return nil
	}
	if err := s.scanner.ScanChunk(ctx, req.GetJobId(), req.GetData()); err != nil {
		var violation *dlp.Violation
		if errors.As(err, &violation) {
			s.logger.Warn().
				Str("job_id", req.GetJobId()).
				Str("rule", violation.Rule).
				Int("chunk_index", int(req.GetChunkIndex())).
				Msg("dlp violation on chunk")
			if s.scanner.Enforced() {
				return status.Error(codes.PermissionDenied, violation.Error())
			}
			return nil
		}
		return status.Errorf(codes.Internal, "chunk scan failed: %v", err)
	}
	return nil
}

func (s *ingestServer) fanoutManifest(ctx context.Context, manifest *manifestpb.Manifest, payload []byte) error {
	for _, conn := range s.connectors {
		if err := conn.StoreManifest(ctx, manifest, payload); err != nil {
			s.logger.Error().
				Err(err).
				Str("connector", conn.Name()).
				Str("job_id", manifest.GetJobId()).
				Msg("connector failed to store manifest")
			if s.connectorStrict {
				return fmt.Errorf("connector %s manifest: %w", conn.Name(), err)
			}
		}
	}
	return nil
}

func (s *ingestServer) fanoutChunk(ctx context.Context, req *manifestpb.UploadChunkRequest) error {
	for _, conn := range s.connectors {
		if err := conn.StoreChunk(ctx, req.GetJobId(), req.GetChunkIndex(), req.GetData()); err != nil {
			s.logger.Error().
				Err(err).
				Str("connector", conn.Name()).
				Str("job_id", req.GetJobId()).
				Int("chunk_index", int(req.GetChunkIndex())).
				Msg("connector failed to store chunk")
			if s.connectorStrict {
				return fmt.Errorf("connector %s chunk: %w", conn.Name(), err)
			}
		}
	}
	return nil
}

func migrate(ctx context.Context, pool *pgxpool.Pool) error {
	const stmt = `
        CREATE TABLE IF NOT EXISTS transfers (
            id UUID PRIMARY KEY,
            job_id TEXT UNIQUE NOT NULL,
            status TEXT NOT NULL,
            created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
            updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
        );`
	_, err := pool.Exec(ctx, stmt)
	return err
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func boolEnv(key string, def bool) bool {
	if v := os.Getenv(key); v != "" {
		if parsed, err := strconv.ParseBool(strings.TrimSpace(v)); err == nil {
			return parsed
		}
	}
	return def
}
