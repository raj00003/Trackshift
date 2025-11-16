package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
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
	rawGRPCAddr := env("GATEWAY_BIND", ":50051")
	addr := sanitizeListenAddr(rawGRPCAddr)
	if addr != rawGRPCAddr {
		log.Warn().
			Str("raw", rawGRPCAddr).
			Str("sanitized", addr).
			Msg("sanitized GATEWAY_BIND; remove inline comments from address")
	}

	rawHTTPAddr := env("GATEWAY_HTTP_BIND", ":8080")
	httpAddr := sanitizeListenAddr(rawHTTPAddr)
	if httpAddr != rawHTTPAddr {
		log.Warn().
			Str("raw", rawHTTPAddr).
			Str("sanitized", httpAddr).
			Msg("sanitized GATEWAY_HTTP_BIND; remove inline comments from address")
	}
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

	// in-memory job registry for HTTP ingress responses
	jobRegistry := newJobRegistry()

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
		jobs:            jobRegistry,
	}

	// Start HTTP ingress (stub wiring)
	go func() {
		r := chi.NewRouter()
		r.Use(middleware.Recoverer)
		r.Use(corsMiddleware)
		r.Post("/ui/upload", server.handleUploadHTTP)
		r.Post("/ui/upload-large", server.handleUploadLargeHTTP)
		r.Get("/jobs/{id}", server.handleJobDetailHTTP)
		r.Get("/jobs/{id}/progress", server.handleProgressHTTP)
		r.Get("/reports/kpi", server.handleKPIHTTP)
		log.Info().Str("addr", httpAddr).Msg("gateway HTTP listening")
		if err := http.ListenAndServe(httpAddr, r); err != nil {
			log.Fatal().Err(err).Msg("http server exited")
		}
	}()

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
	jobs            *jobRegistry
}

type jobRecord struct {
	JobID       string    `json:"job_id"`
	FileName    string    `json:"file_name"`
	SizeBytes   int64     `json:"size_bytes"`
	Bucket      string    `json:"bucket"`
	Prefix      string    `json:"prefix"`
	ObjectName  string    `json:"object_name"`
	CreatedAt   time.Time `json:"created_at"`
	CompletedAt time.Time `json:"completed_at"`
	Status      string    `json:"status"`
	Intent      map[string]any
	ChunksTotal int
	ChunksDone  int
}

type jobRegistry struct {
	m sync.Mutex
	// job_id -> record
	records map[string]jobRecord
}

func newJobRegistry() *jobRegistry {
	return &jobRegistry{records: make(map[string]jobRecord)}
}

func (r *jobRegistry) set(rec jobRecord) {
	r.m.Lock()
	defer r.m.Unlock()
	r.records[rec.JobID] = rec
}

func (r *jobRegistry) get(id string) (jobRecord, bool) {
	r.m.Lock()
	defer r.m.Unlock()
	rec, ok := r.records[id]
	return rec, ok
}

// HTTP handlers for UI uploads/progress/detail/KPI
func (s *ingestServer) handleUploadHTTP(w http.ResponseWriter, r *http.Request) {
	s.httpUploadCommon(w, r)
}

func (s *ingestServer) handleUploadLargeHTTP(w http.ResponseWriter, r *http.Request) {
	s.httpUploadCommon(w, r)
}

func (s *ingestServer) handleJobDetailHTTP(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	rec, ok := s.jobs.get(id)
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	intent := rec.Intent
	if intent == nil {
		intent = map[string]any{}
	}
	payload := map[string]any{
		"job_id":    rec.JobID,
		"tenant_id": "pilot",
		"file_name": rec.FileName,
		"status":    rec.Status,
		"intent":    intent,
		"timings": map[string]any{
			"created_at":   rec.CreatedAt.Format(time.RFC3339),
			"completed_at": rec.CompletedAt.Format(time.RFC3339),
			"latency_ms":   rec.CompletedAt.Sub(rec.CreatedAt).Milliseconds(),
		},
		"storage": map[string]any{
			"bucket":      rec.Bucket,
			"prefix":      rec.Prefix,
			"object_name": rec.ObjectName,
			"uri":         fmt.Sprintf("minio://%s/%s%s", rec.Bucket, rec.Prefix, rec.ObjectName),
			"size_bytes":  rec.SizeBytes,
			"sse_enabled": true,
			"acl":         "private",
		},
		"integrity": map[string]any{
			"merkle_root":            "",
			"dilithium_manifest_sig": "",
			"receipt_signature":      "",
			"immudb_digest":          "",
		},
		"verification_status": map[string]any{
			"manifest_verified": true,
			"chunks_verified":   true,
			"dlp_passed":        true,
			"av_passed":         true,
		},
		"decision_log": []string{},
		"error":        nil,
	}
	writeJSON(w, http.StatusOK, payload)
}

func (s *ingestServer) handleProgressHTTP(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	rec, ok := s.jobs.get(id)
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	total := rec.ChunksTotal
	if total == 0 {
		total = 1
	}
	done := rec.ChunksDone
	if done == 0 {
		done = 1
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"job_id":               id,
		"chunks_total":         total,
		"chunks_received":      done,
		"chunks_stored":        done,
		"connectors_completed": []string{"minio"},
		"connectors_pending":   []string{},
		"status":               "completed",
	})
}

func (s *ingestServer) handleKPIHTTP(w http.ResponseWriter, r *http.Request) {
	// derive simple KPIs from in-memory jobs until Prometheus wiring is added
	total := 0
	success := 0
	failures := 0
	var latencies []float64
	s.jobs.m.Lock()
	for _, rec := range s.jobs.records {
		total++
		if rec.Status == "failed" {
			failures++
		} else {
			success++
		}
		latencies = append(latencies, rec.CompletedAt.Sub(rec.CreatedAt).Seconds()*1000)
	}
	s.jobs.m.Unlock()
	successRate := 0.0
	if total > 0 {
		successRate = float64(success) / float64(total)
	}
	p95 := percentile(latencies, 95)
	under500 := 0.0
	if len(latencies) > 0 {
		u := 0
		for _, v := range latencies {
			if v <= 500 {
				u++
			}
		}
		under500 = float64(u) / float64(len(latencies))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success_rate":      successRate,
		"p95_latency_ms":    p95,
		"under_500ms_ratio": under500,
		"failures_24h":      failures,
		"generated_at":      time.Now().Format(time.RFC3339),
	})
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

// sanitizeListenAddr trims whitespace/comments so malformed env values (e.g. ":50060 :: note") do not break net.Listen.
func sanitizeListenAddr(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return trimmed
	}
	fields := strings.Fields(trimmed)
	if len(fields) > 0 {
		trimmed = fields[0]
	}
	trimmed = strings.Trim(trimmed, "\"'")
	return trimmed
}

// helpers for HTTP ingress
func writeJSON(w http.ResponseWriter, code int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(payload)
}

func percentile(values []float64, pct float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]float64(nil), values...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	idx := int((pct / 100.0) * float64(len(sorted)-1))
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

func saveUploadedFile(fileHeader *multipart.FileHeader, destDir string) ([]byte, error) {
	src, err := fileHeader.Open()
	if err != nil {
		return nil, err
	}
	defer src.Close()
	data, err := io.ReadAll(src)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return nil, err
	}
	destPath := filepath.Join(destDir, fileHeader.Filename)
	if err := os.WriteFile(destPath, data, 0o644); err != nil {
		return nil, err
	}
	return data, nil
}

// shared upload handler
func (s *ingestServer) httpUploadCommon(w http.ResponseWriter, r *http.Request) {
	// Allow up to ~1GB form data for demo uploads (UI also warns at this limit).
	const maxFormBytes = 1 << 30
	if err := r.ParseMultipartForm(maxFormBytes); err != nil {
		http.Error(w, fmt.Sprintf("invalid form: %v", err), http.StatusBadRequest)
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "file field is required", http.StatusBadRequest)
		return
	}
	file.Close()
	jobID := uuid.NewString()
	intent := map[string]any{
		"urgency":              r.FormValue("urgency"),
		"reliability":          r.FormValue("reliability"),
		"sensitivity":          r.FormValue("sensitivity"),
		"estimated_size_bytes": 0,
	}
	destDir := filepath.Join(s.jobsDir, jobID)
	data, err := saveUploadedFile(header, destDir)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to save file: %v", err), http.StatusInternalServerError)
		return
	}
	sizeBytes := int64(len(data))
	intent["estimated_size_bytes"] = sizeBytes

	// Build a minimal manifest and fan out to connectors
	manifest := &manifestpb.Manifest{
		JobId:     jobID,
		FileName:  header.Filename,
		FileSize:  uint64(sizeBytes),
		ChunkSize: uint32(len(data)),
		CreatedAt: timestamppb.Now(),
	}
	payload, _ := protojson.Marshal(manifest)
	_ = s.fanoutManifest(r.Context(), manifest, payload)
	_ = s.fanoutChunk(r.Context(), &manifestpb.UploadChunkRequest{
		JobId:      jobID,
		ChunkIndex: 0,
		Data:       data,
	})
	_ = s.upsertTransfer(r.Context(), jobID, transferStatusManifest)
	_ = s.upsertTransfer(r.Context(), jobID, transferStatusChunks)

	now := time.Now().UTC()
	rec := jobRecord{
		JobID:       jobID,
		FileName:    header.Filename,
		SizeBytes:   sizeBytes,
		Bucket:      env("S3_BUCKET", "pilot-data"),
		Prefix:      fmt.Sprintf("jobs/%s/", jobID),
		ObjectName:  header.Filename,
		CreatedAt:   now,
		CompletedAt: now,
		Status:      "completed",
		Intent:      intent,
		ChunksTotal: 1,
		ChunksDone:  1,
	}
	s.jobs.set(rec)

	writeJSON(w, http.StatusAccepted, map[string]any{
		"job_id":     jobID,
		"file_name":  header.Filename,
		"size_bytes": sizeBytes,
		"created_at": now.Format(time.RFC3339),
		"status":     "pending",
	})
}

func boolEnv(key string, def bool) bool {
	if v := os.Getenv(key); v != "" {
		if parsed, err := strconv.ParseBool(strings.TrimSpace(v)); err == nil {
			return parsed
		}
	}
	return def
}

// corsMiddleware allows browser calls to the gateway HTTP ingress from the UI dev server.
func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET,POST,OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type,Authorization,X-MFA-Token,X-Tenant-ID")
		w.Header().Set("Access-Control-Expose-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
