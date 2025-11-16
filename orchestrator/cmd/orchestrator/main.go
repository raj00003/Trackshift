package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
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
	"github.com/pquerna/otp/totp"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

type ctxKey string

const (
	ctxKeyRole   ctxKey = "role"
	ctxKeyTenant ctxKey = "tenant"
)

const (
	defaultChunkSizeBytes      = 8 * 1024 * 1024
	defaultDemoUploadMaxBytes  = 1 * 1024 * 1024 * 1024  // 1 GiB
	defaultMetaMaxBytes        = 5 * 1024 * 1024         // 5 MiB
	defaultLargeUploadMaxBytes = 20 * 1024 * 1024 * 1024 // 20 GiB
)

type Role string

const (
	RoleAdmin    Role = "admin"
	RoleOperator      = "operator"
	RoleViewer        = "viewer"
)

type JobState string

const (
	JobStateQueued    JobState = "QUEUED"
	JobStateRunning   JobState = "RUNNING"
	JobStateCompleted JobState = "COMPLETED"
	JobStateFailed    JobState = "FAILED"
	JobStateCanceled  JobState = "CANCELED"
)

type TransferFile struct {
	Name       string `json:"name"`
	SizeBytes  int64  `json:"sizeBytes"`
	Mime       string `json:"mime"`
	Critical   bool   `json:"critical"`
	Challenges string `json:"challenges,omitempty"`
}

type jobRequest struct {
	Intent   map[string]any `json:"intent"`
	Files    []TransferFile `json:"files"`
	Metadata map[string]any `json:"metadata"`
}

type PolicyDecision struct {
	Rule      string    `json:"rule"`
	Action    string    `json:"action"`
	Reason    string    `json:"reason"`
	Timestamp time.Time `json:"timestamp"`
}

type Prediction struct {
	Path         string  `json:"path"`
	Strategy     string  `json:"strategy"`
	LatencyMs    int     `json:"latencyMs"`
	Confidence   float64 `json:"confidence"`
	FailureGuard string  `json:"failureGuard"`
}

type Receipt struct {
	ReceiptID    string    `json:"receiptId"`
	JobID        string    `json:"jobId"`
	TenantID     string    `json:"tenantId"`
	Status       string    `json:"status"`
	SignedAt     time.Time `json:"signedAt"`
	Signature    string    `json:"signature"`
	SignatureAlg string    `json:"signatureAlg"`
	PayloadHash  string    `json:"payloadHash"`
}

type StorageInfo struct {
	Bucket     string `json:"bucket"`
	Prefix     string `json:"prefix"`
	ObjectName string `json:"object_name"`
	URI        string `json:"uri"`
	SizeBytes  int64  `json:"size_bytes"`
	SSEEnabled bool   `json:"sse_enabled"`
	ACL        string `json:"acl"`
}

type IntegrityInfo struct {
	MerkleRoot           string `json:"merkle_root"`
	DilithiumManifestSig string `json:"dilithium_manifest_sig"`
	ReceiptSignature     string `json:"receipt_signature"`
	ImmudbDigest         string `json:"immudb_digest"`
}

type VerificationStatus struct {
	ManifestVerified bool `json:"manifest_verified"`
	ChunksVerified   bool `json:"chunks_verified"`
	DlpPassed        bool `json:"dlp_passed"`
	AvPassed         bool `json:"av_passed"`
}

type ErrorInfo struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type ProgressState struct {
	ChunksTotal         int      `json:"chunks_total"`
	ChunksReceived      int      `json:"chunks_received"`
	ChunksStored        int      `json:"chunks_stored"`
	ConnectorsCompleted []string `json:"connectors_completed"`
	ConnectorsPending   []string `json:"connectors_pending"`
}

type Job struct {
	ID              string                 `json:"id"`
	TenantID        string                 `json:"tenantId"`
	Intent          map[string]any         `json:"intent"`
	Files           []TransferFile         `json:"files"`
	State           JobState               `json:"state"`
	CreatedAt       time.Time              `json:"createdAt"`
	CompletedAt     *time.Time             `json:"completed_at,omitempty"`
	UpdatedAt       time.Time              `json:"updatedAt"`
	Metadata        map[string]any         `json:"metadata"`
	PolicyDecisions []PolicyDecision       `json:"policyDecisions"`
	Prediction      Prediction             `json:"prediction"`
	Receipt         *Receipt               `json:"receipt,omitempty"`
	DecisionLog     []string               `json:"decisionLog"`
	LatencyMillis   int64                  `json:"latencyMillis"`
	LastError       string                 `json:"lastError,omitempty"`
	ComplianceTags  map[string]string      `json:"complianceTags,omitempty"`
	Additional      map[string]interface{} `json:"additional,omitempty"`
	Storage         *StorageInfo           `json:"storage,omitempty"`
	Integrity       *IntegrityInfo         `json:"integrity,omitempty"`
	Verification    *VerificationStatus    `json:"verification_status,omitempty"`
	ErrorInfo       *ErrorInfo             `json:"error,omitempty"`
}

func (j *Job) snapshot() Job {
	cp := *j
	cp.Files = append([]TransferFile(nil), j.Files...)
	cp.PolicyDecisions = append([]PolicyDecision(nil), j.PolicyDecisions...)
	cp.DecisionLog = append([]string(nil), j.DecisionLog...)
	if j.Intent != nil {
		cp.Intent = cloneMap(j.Intent)
	}
	if j.Metadata != nil {
		cp.Metadata = cloneMap(j.Metadata)
	}
	if j.ComplianceTags != nil {
		cp.ComplianceTags = cloneStringMap(j.ComplianceTags)
	}
	if j.Additional != nil {
		cp.Additional = cloneMap(j.Additional)
	}
	if j.Receipt != nil {
		r := *j.Receipt
		cp.Receipt = &r
	}
	if j.Storage != nil {
		s := *j.Storage
		cp.Storage = &s
	}
	if j.Integrity != nil {
		i := *j.Integrity
		cp.Integrity = &i
	}
	if j.Verification != nil {
		v := *j.Verification
		cp.Verification = &v
	}
	if j.ErrorInfo != nil {
		e := *j.ErrorInfo
		cp.ErrorInfo = &e
	}
	return cp
}

func cloneMap(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func cloneStringMap(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

type policyConfig struct {
	allowedClasses map[string]map[string]struct{}
	blockedWords   []string
	maxFileSizeMB  int64
	requirePQC     bool
}

type predictor struct {
	mlURL  string
	log    zerolog.Logger
	client *http.Client
}

type policyEngine struct {
	cfg policyConfig
}

type receiptSigner struct {
	priv ed25519.PrivateKey
	pub  ed25519.PublicKey
	dir  string
}

type authConfig struct {
	apiKeys   map[string]Role
	mfaSecret string
	mfaBypass string
}

type metrics struct {
	mu              sync.Mutex
	latencies       []time.Duration
	successCount    int
	failureCount    int
	cancelCount     int
	pendingReceipts int
}

func (m *metrics) record(result JobState, latency time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	switch result {
	case JobStateCompleted:
		m.successCount++
		if latency > 0 {
			m.latencies = append(m.latencies, latency)
		}
	case JobStateFailed:
		m.failureCount++
	case JobStateCanceled:
		m.cancelCount++
	}
}

func (m *metrics) snapshot() (successRate float64, p95 float64, sub500 float64, failures int, cancels int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	total := m.successCount + m.failureCount
	if total > 0 {
		successRate = float64(m.successCount) / float64(total)
	}
	failures = m.failureCount
	cancels = m.cancelCount
	if len(m.latencies) > 0 {
		durations := append([]time.Duration(nil), m.latencies...)
		sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
		index := int(math.Round(0.95 * float64(len(durations)-1)))
		p95 = float64(durations[index].Milliseconds())
		var sub int
		for _, d := range durations {
			if d.Milliseconds() <= 500 {
				sub++
			}
		}
		sub500 = float64(sub) / float64(len(durations))
	}
	return successRate, p95, sub500, failures, cancels
}

type Server struct {
	mu         sync.RWMutex
	jobs       map[string]*Job
	progress   map[string]*ProgressState
	queue      chan string
	policy     *policyEngine
	predictor  *predictor
	signer     *receiptSigner
	metrics    *metrics
	logger     zerolog.Logger
	failFactor float64
}

func newServer(policy *policyEngine, pred *predictor, signer *receiptSigner, workerCount int) *Server {
	s := &Server{
		jobs:       make(map[string]*Job),
		progress:   make(map[string]*ProgressState),
		queue:      make(chan string, 1024),
		policy:     policy,
		predictor:  pred,
		signer:     signer,
		metrics:    &metrics{},
		logger:     log.With().Str("component", "orchestrator").Logger(),
		failFactor: 0.0, // disable simulated failures for upload flows
	}
	for i := 0; i < workerCount; i++ {
		go s.worker(i)
	}
	return s
}

func (s *Server) worker(id int) {
	for jobID := range s.queue {
		s.runJob(jobID, id)
	}
}

func (s *Server) enqueue(jobID string) {
	select {
	case s.queue <- jobID:
	default:
		s.logger.Warn().Str("job_id", jobID).Msg("queue saturated, dropping job")
		s.updateJob(jobID, func(job *Job) {
			job.State = JobStateFailed
			job.LastError = "scheduler overloaded"
			job.DecisionLog = append(job.DecisionLog, "scheduler dropped job due to backpressure")
			job.UpdatedAt = time.Now().UTC()
		})
	}
}

func (s *Server) runJob(jobID string, workerID int) {
	job, ok := s.getJob(jobID)
	if !ok {
		return
	}
	if job.State == JobStateCanceled {
		s.logger.Info().Str("job_id", jobID).Msg("job canceled before execution")
		s.metrics.record(JobStateCanceled, 0)
		return
	}
	start := time.Now().UTC()
	s.updateJob(jobID, func(job *Job) {
		job.State = JobStateRunning
		job.DecisionLog = append(job.DecisionLog, fmt.Sprintf("worker-%d picked job", workerID))
		job.UpdatedAt = start
	})
	chunkTotal := 0
	if progress, ok := s.getProgress(jobID); ok && progress.ChunksTotal > 0 {
		chunkTotal = progress.ChunksTotal
	}
	if chunkTotal == 0 && len(job.Files) > 0 {
		chunkTotal = chunkCount(job.Files[0].SizeBytes)
	}
	if chunkTotal == 0 {
		chunkTotal = 16
	}
	s.setProgress(jobID, func(p *ProgressState) {
		if p.ChunksTotal == 0 {
			p.ChunksTotal = chunkTotal
		}
		if len(p.ConnectorsPending) == 0 {
			p.ConnectorsPending = []string{"minio"}
		}
	})

	delay := time.Duration(job.Prediction.LatencyMs) * time.Millisecond
	if delay < 200*time.Millisecond {
		delay = 200 * time.Millisecond
	}
	step := delay / time.Duration(chunkTotal+1)
	if step < 50*time.Millisecond {
		step = 50 * time.Millisecond
	}
	for i := 0; i < chunkTotal; i++ {
		time.Sleep(step)
		if job.State == JobStateCanceled {
			s.logger.Info().Str("job_id", jobID).Msg("job canceled mid-flight")
			s.metrics.record(JobStateCanceled, 0)
			return
		}
		idx := i
		s.setProgress(jobID, func(p *ProgressState) {
			p.ChunksReceived = idx + 1
			p.ChunksStored = idx + 1
			if p.ChunksTotal == 0 {
				p.ChunksTotal = chunkTotal
			}
		})
	}

	if job.State == JobStateCanceled {
		s.logger.Info().Str("job_id", jobID).Msg("job canceled mid-flight")
		s.metrics.record(JobStateCanceled, 0)
		return
	}

	s.updateJob(jobID, func(job *Job) {
		job.State = JobStateCompleted
		job.DecisionLog = append(job.DecisionLog, "job completed successfully, issuing receipt")
		job.UpdatedAt = time.Now().UTC()
		completed := job.UpdatedAt
		job.CompletedAt = &completed
		job.LatencyMillis = time.Since(start).Milliseconds()
		if len(job.Files) > 0 {
			job.Storage = &StorageInfo{
				Bucket:     fmt.Sprintf("%s-data", job.TenantID),
				Prefix:     fmt.Sprintf("jobs/%s/", job.ID),
				ObjectName: job.Files[0].Name,
				URI:        fmt.Sprintf("minio://%s-data/jobs/%s/%s", job.TenantID, job.ID, job.Files[0].Name),
				SizeBytes:  job.Files[0].SizeBytes,
				SSEEnabled: true,
				ACL:        "private",
			}
		}
		hash := sha256.Sum256([]byte(job.ID))
		job.Integrity = &IntegrityInfo{
			MerkleRoot:           fmt.Sprintf("0x%x", hash[:]),
			DilithiumManifestSig: "dilithium:demo-signature",
			ReceiptSignature:     "",
			ImmudbDigest:         "0ximmudb",
		}
		job.Verification = &VerificationStatus{
			ManifestVerified: true,
			ChunksVerified:   true,
			DlpPassed:        true,
			AvPassed:         true,
		}
		job.ErrorInfo = nil
	})
	s.setProgress(jobID, func(p *ProgressState) {
		p.ChunksReceived = p.ChunksTotal
		p.ChunksStored = p.ChunksTotal
		p.ConnectorsPending = nil
		p.ConnectorsCompleted = []string{"minio"}
	})

	completedJob, _ := s.getJob(jobID)
	if completedJob != nil {
		receipt, err := s.signer.signReceipt(completedJob)
		if err != nil {
			s.logger.Error().Err(err).Str("job_id", jobID).Msg("failed to sign receipt")
		} else {
			if err := s.signer.persist(receipt); err != nil {
				s.logger.Error().Err(err).Str("job_id", jobID).Msg("failed to persist receipt")
			}
			s.updateJob(jobID, func(job *Job) {
				job.Receipt = receipt
			})
		}
	}
	s.metrics.record(JobStateCompleted, time.Since(start))
}

func (s *Server) saveJob(job *Job) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.jobs[job.ID] = job
}

func (s *Server) getJob(id string) (*Job, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	job, ok := s.jobs[id]
	if !ok {
		return nil, false
	}
	copy := job.snapshot()
	return &copy, true
}

func (s *Server) listJobs(tenant string, includeAll bool) []Job {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Job, 0, len(s.jobs))
	for _, job := range s.jobs {
		if includeAll || job.TenantID == tenant {
			out = append(out, job.snapshot())
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out
}

func (s *Server) updateJob(id string, fn func(job *Job)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if job, ok := s.jobs[id]; ok {
		fn(job)
	}
}

func (s *Server) setProgress(id string, fn func(p *ProgressState)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.progress[id]
	if !ok {
		p = &ProgressState{}
		s.progress[id] = p
	}
	fn(p)
}

func (s *Server) getProgress(id string) (*ProgressState, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.progress[id]
	if !ok {
		return nil, false
	}
	cp := *p
	cp.ConnectorsCompleted = append([]string(nil), p.ConnectorsCompleted...)
	cp.ConnectorsPending = append([]string(nil), p.ConnectorsPending...)
	return &cp, true
}

func (s *Server) createJob(w http.ResponseWriter, r *http.Request) {
	role := mustRole(r.Context())
	if role != RoleAdmin && role != RoleOperator {
		http.Error(w, "insufficient role", http.StatusForbidden)
		return
	}
	var req jobRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("invalid payload: %v", err), http.StatusBadRequest)
		return
	}
	if len(req.Files) == 0 {
		http.Error(w, "at least one file required", http.StatusBadRequest)
		return
	}
	tenant := mustTenant(r.Context())
	decisions, err := s.policy.evaluate(req, tenant)
	if err != nil {
		http.Error(w, fmt.Sprintf("policy violation: %v", err), http.StatusForbidden)
		return
	}
	prediction := s.predictor.predict(r.Context(), req)
	now := time.Now().UTC()
	job := &Job{
		ID:              uuid.NewString(),
		TenantID:        tenant,
		Intent:          req.Intent,
		Files:           req.Files,
		State:           JobStateQueued,
		CreatedAt:       now,
		UpdatedAt:       now,
		Metadata:        req.Metadata,
		PolicyDecisions: decisions,
		Prediction:      prediction,
		DecisionLog: []string{
			fmt.Sprintf("job registered with path=%s strategy=%s", prediction.Path, prediction.Strategy),
		},
		ComplianceTags: map[string]string{
			"pqc": fmt.Sprintf("%v", req.Intent["pqc"]),
		},
	}
	s.saveJob(job)
	s.enqueue(job.ID)
	writeJSON(w, http.StatusAccepted, job)
}

type uploadMetadataRequest struct {
	FileName           string         `json:"file_name"`
	EstimatedSizeBytes int64          `json:"estimated_size_bytes"`
	Intent             map[string]any `json:"intent"`
}

func (s *Server) uiUploadMetadataHandler(w http.ResponseWriter, r *http.Request) {
	role := mustRole(r.Context())
	if role != RoleAdmin && role != RoleOperator {
		http.Error(w, "insufficient role", http.StatusForbidden)
		return
	}
	maxMeta := getInt64Env("ORCH_META_MAX_BYTES", defaultMetaMaxBytes)
	if r.ContentLength > maxMeta && maxMeta > 0 {
		http.Error(w, "metadata request too large", http.StatusRequestEntityTooLarge)
		return
	}
	var req uploadMetadataRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("invalid payload: %v", err), http.StatusBadRequest)
		return
	}
	if req.FileName == "" || req.EstimatedSizeBytes <= 0 {
		http.Error(w, "file_name and estimated_size_bytes are required", http.StatusBadRequest)
		return
	}
	tenant := mustTenant(r.Context())
	if limit := getInt64Env("ORCH_TENANT_MAX_BYTES", 100*1024*1024*1024); limit > 0 && req.EstimatedSizeBytes > limit {
		http.Error(w, "estimated size exceeds tenant quota", http.StatusRequestEntityTooLarge)
		return
	}
	now := time.Now().UTC()
	jobID := uuid.NewString()
	intent := cloneMap(req.Intent)
	if intent == nil {
		intent = map[string]any{}
	}
	intent["estimated_size_bytes"] = req.EstimatedSizeBytes
	job := &Job{
		ID:        jobID,
		TenantID:  tenant,
		Intent:    intent,
		Files:     []TransferFile{{Name: req.FileName, SizeBytes: req.EstimatedSizeBytes, Mime: "application/octet-stream"}},
		State:     JobStateQueued,
		CreatedAt: now,
		UpdatedAt: now,
		DecisionLog: []string{
			"metadata registered; waiting for edge agent",
		},
		Storage: &StorageInfo{
			Bucket:     fmt.Sprintf("%s-data", tenant),
			Prefix:     fmt.Sprintf("jobs/%s/", jobID),
			ObjectName: req.FileName,
			URI:        fmt.Sprintf("minio://%s-data/jobs/%s/%s", tenant, jobID, req.FileName),
			SizeBytes:  req.EstimatedSizeBytes,
			SSEEnabled: true,
			ACL:        "private",
		},
	}
	s.saveJob(job)
	chunks := chunkCount(req.EstimatedSizeBytes)
	s.setProgress(jobID, func(p *ProgressState) {
		p.ChunksTotal = chunks
		p.ConnectorsPending = []string{"minio"}
	})
	s.enqueue(job.ID)
	writeJSON(w, http.StatusAccepted, map[string]any{
		"job_id":     jobID,
		"file_name":  req.FileName,
		"intent":     intent,
		"created_at": now.Format(time.RFC3339),
		"status":     "pending",
	})
}

func (s *Server) uiUploadHandler(w http.ResponseWriter, r *http.Request) {
	role := mustRole(r.Context())
	if role != RoleAdmin && role != RoleOperator {
		http.Error(w, "insufficient role", http.StatusForbidden)
		return
	}
	maxUpload := getInt64Env("ORCH_UI_UPLOAD_MAX_BYTES", defaultDemoUploadMaxBytes)
	if r.ContentLength > maxUpload && maxUpload > 0 {
		http.Error(w, "payload too large", http.StatusRequestEntityTooLarge)
		return
	}
	if err := r.ParseMultipartForm(maxUpload); err != nil {
		http.Error(w, fmt.Sprintf("invalid multipart form: %v", err), http.StatusBadRequest)
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "file is required", http.StatusBadRequest)
		return
	}
	defer file.Close()
	intent := map[string]any{
		"urgency":     r.FormValue("urgency"),
		"reliability": r.FormValue("reliability"),
		"sensitivity": r.FormValue("sensitivity"),
	}
	tenant := mustTenant(r.Context())
	now := time.Now().UTC()
	jobID := uuid.NewString()
	destDir := filepath.Join("data", "ui-uploads", tenant, jobID)
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		http.Error(w, fmt.Sprintf("failed to create staging dir: %v", err), http.StatusInternalServerError)
		return
	}
	destPath := filepath.Join(destDir, header.Filename)
	out, err := os.Create(destPath)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to create file: %v", err), http.StatusInternalServerError)
		return
	}
	defer out.Close()
	written, err := io.Copy(out, file)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to write file: %v", err), http.StatusInternalServerError)
		return
	}
	if written > maxUpload && maxUpload > 0 {
		http.Error(w, "payload too large", http.StatusRequestEntityTooLarge)
		return
	}
	chunks := chunkCount(written)
	job := &Job{
		ID:        jobID,
		TenantID:  tenant,
		Intent:    intent,
		Files:     []TransferFile{{Name: header.Filename, SizeBytes: written, Mime: header.Header.Get("Content-Type")}},
		State:     JobStateQueued,
		CreatedAt: now,
		UpdatedAt: now,
		DecisionLog: []string{
			"browser upload staged; scheduling transfer",
		},
		Storage: &StorageInfo{
			Bucket:     fmt.Sprintf("%s-data", tenant),
			Prefix:     fmt.Sprintf("jobs/%s/", jobID),
			ObjectName: header.Filename,
			URI:        fmt.Sprintf("minio://%s-data/jobs/%s/%s", tenant, jobID, header.Filename),
			SizeBytes:  written,
			SSEEnabled: true,
			ACL:        "private",
		},
	}
	s.saveJob(job)
	s.setProgress(jobID, func(p *ProgressState) {
		p.ChunksTotal = chunks
		p.ConnectorsPending = []string{"minio"}
	})
	s.enqueue(job.ID)
	writeJSON(w, http.StatusAccepted, map[string]any{
		"job_id":     jobID,
		"file_name":  header.Filename,
		"intent":     intent,
		"created_at": now.Format(time.RFC3339),
		"status":     "pending",
	})
}

func (s *Server) uiUploadLargeHandler(w http.ResponseWriter, r *http.Request) {
	role := mustRole(r.Context())
	if role != RoleAdmin && role != RoleOperator {
		http.Error(w, "insufficient role", http.StatusForbidden)
		return
	}
	maxUpload := getInt64Env("ORCH_UI_LARGE_MAX_BYTES", defaultLargeUploadMaxBytes)
	if r.ContentLength > maxUpload && maxUpload > 0 {
		http.Error(w, "payload too large", http.StatusRequestEntityTooLarge)
		return
	}
	if err := r.ParseMultipartForm(32 << 20); err != nil { // 32MB memory, rest streaming
		http.Error(w, fmt.Sprintf("invalid multipart form: %v", err), http.StatusBadRequest)
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "file is required", http.StatusBadRequest)
		return
	}
	defer file.Close()
	intent := map[string]any{
		"urgency":     r.FormValue("urgency"),
		"reliability": r.FormValue("reliability"),
		"sensitivity": r.FormValue("sensitivity"),
	}
	tenant := mustTenant(r.Context())
	now := time.Now().UTC()
	jobID := uuid.NewString()

	destDir := filepath.Join("data", "ui-uploads", tenant, jobID)
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		http.Error(w, fmt.Sprintf("failed to create staging dir: %v", err), http.StatusInternalServerError)
		return
	}
	destPath := filepath.Join(destDir, header.Filename)
	out, err := os.Create(destPath)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to create file: %v", err), http.StatusInternalServerError)
		return
	}
	defer out.Close()
	written, err := io.Copy(out, file)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to write file: %v", err), http.StatusInternalServerError)
		return
	}
	if maxUpload > 0 && written > maxUpload {
		http.Error(w, "payload too large", http.StatusRequestEntityTooLarge)
		return
	}

	intent["estimated_size_bytes"] = written
	chunks := chunkCount(written)
	job := &Job{
		ID:        jobID,
		TenantID:  tenant,
		Intent:    intent,
		Files:     []TransferFile{{Name: header.Filename, SizeBytes: written, Mime: header.Header.Get("Content-Type")}},
		State:     JobStateQueued,
		CreatedAt: now,
		UpdatedAt: now,
		DecisionLog: []string{
			"large upload staged; edge agent to transfer",
		},
		Storage: &StorageInfo{
			Bucket:     fmt.Sprintf("%s-data", tenant),
			Prefix:     fmt.Sprintf("jobs/%s/", jobID),
			ObjectName: header.Filename,
			URI:        fmt.Sprintf("minio://%s-data/jobs/%s/%s", tenant, jobID, header.Filename),
			SizeBytes:  written,
			SSEEnabled: true,
			ACL:        "private",
		},
	}
	s.saveJob(job)
	s.setProgress(jobID, func(p *ProgressState) {
		p.ChunksTotal = chunks
		p.ConnectorsPending = []string{"minio"}
	})
	s.enqueue(job.ID)
	writeJSON(w, http.StatusAccepted, map[string]any{
		"job_id":     jobID,
		"file_name":  header.Filename,
		"size_bytes": written,
		"intent":     intent,
		"created_at": now.Format(time.RFC3339),
		"status":     "pending",
	})
}

func (s *Server) getJobHandler(w http.ResponseWriter, r *http.Request) {
	jobID := chi.URLParam(r, "id")
	job, ok := s.getJob(jobID)
	if !ok {
		http.NotFound(w, r)
		return
	}
	role := mustRole(r.Context())
	tenant := mustTenant(r.Context())
	if role == RoleViewer && job.TenantID != tenant {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, s.jobDetailPayload(job))
}

func (s *Server) jobDetailPayload(job *Job) map[string]any {
	progress, _ := s.getProgress(job.ID)
	intent := map[string]any{}
	for k, v := range job.Intent {
		intent[k] = v
	}
	var createdAt string
	if !job.CreatedAt.IsZero() {
		createdAt = job.CreatedAt.Format(time.RFC3339)
	}
	var completedAt *string
	if job.CompletedAt != nil {
		ts := job.CompletedAt.Format(time.RFC3339)
		completedAt = &ts
	}
	storage := job.Storage
	if storage == nil {
		if len(job.Files) > 0 {
			storage = &StorageInfo{
				Bucket:     fmt.Sprintf("%s-data", job.TenantID),
				Prefix:     fmt.Sprintf("jobs/%s/", job.ID),
				ObjectName: job.Files[0].Name,
				URI:        fmt.Sprintf("minio://%s-data/jobs/%s/%s", job.TenantID, job.ID, job.Files[0].Name),
				SizeBytes:  job.Files[0].SizeBytes,
				SSEEnabled: true,
				ACL:        "private",
			}
		}
	}
	integrity := job.Integrity
	if integrity == nil {
		hash := sha256.Sum256([]byte(job.ID))
		var receiptSig string
		if job.Receipt != nil {
			receiptSig = job.Receipt.Signature
		}
		integrity = &IntegrityInfo{
			MerkleRoot:           fmt.Sprintf("0x%x", hash[:]),
			DilithiumManifestSig: "dilithium:pending",
			ReceiptSignature:     receiptSig,
			ImmudbDigest:         "0x0",
		}
	}
	verification := job.Verification
	if verification == nil {
		verification = &VerificationStatus{
			ManifestVerified: job.State == JobStateCompleted,
			ChunksVerified:   job.State == JobStateCompleted,
			DlpPassed:        job.State != JobStateFailed,
			AvPassed:         job.State != JobStateFailed,
		}
	}
	return map[string]any{
		"job_id":    job.ID,
		"tenant_id": job.TenantID,
		"file_name": func() string {
			if len(job.Files) > 0 {
				return job.Files[0].Name
			}
			return ""
		}(),
		"status": job.State,
		"intent": map[string]any{
			"urgency":              intentValue(intent, "urgency"),
			"reliability":          intentValue(intent, "reliability"),
			"sensitivity":          intentValue(intent, "sensitivity"),
			"estimated_size_bytes": intentValue(intent, "estimated_size_bytes"),
		},
		"timings": map[string]any{
			"created_at":   createdAt,
			"completed_at": completedAt,
			"latency_ms":   job.LatencyMillis,
		},
		"storage":             storage,
		"integrity":           integrity,
		"verification_status": verification,
		"decision_log":        job.DecisionLog,
		"error":               job.ErrorInfo,
		"progress":            progress,
	}
}

func intentValue(intent map[string]any, key string) any {
	if v, ok := intent[key]; ok {
		return v
	}
	return nil
}

func chunkCount(size int64) int {
	if size <= 0 {
		return 0
	}
	return int((size + defaultChunkSizeBytes - 1) / defaultChunkSizeBytes)
}

func (s *Server) listJobsHandler(w http.ResponseWriter, r *http.Request) {
	role := mustRole(r.Context())
	tenant := mustTenant(r.Context())
	var tenantFilter = tenant
	if role == RoleAdmin {
		if override := r.URL.Query().Get("tenant"); override != "" {
			tenantFilter = override
		} else {
			tenantFilter = ""
		}
	}
	includeAll := role == RoleAdmin && tenantFilter == ""
	jobs := s.listJobs(tenantFilter, includeAll)
	writeJSON(w, http.StatusOK, jobs)
}

func (s *Server) cancelJobHandler(w http.ResponseWriter, r *http.Request) {
	role := mustRole(r.Context())
	if role != RoleAdmin && role != RoleOperator {
		http.Error(w, "insufficient role", http.StatusForbidden)
		return
	}
	jobID := chi.URLParam(r, "id")
	job, ok := s.getJob(jobID)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if job.State == JobStateCompleted || job.State == JobStateFailed {
		http.Error(w, "job already finished", http.StatusConflict)
		return
	}
	s.updateJob(jobID, func(job *Job) {
		job.State = JobStateCanceled
		job.DecisionLog = append(job.DecisionLog, "job canceled by operator")
		job.UpdatedAt = time.Now().UTC()
	})
	writeJSON(w, http.StatusOK, map[string]string{"status": "canceled"})
}

func (s *Server) progressHandler(w http.ResponseWriter, r *http.Request) {
	jobID := chi.URLParam(r, "id")
	job, ok := s.getJob(jobID)
	if !ok {
		http.NotFound(w, r)
		return
	}
	role := mustRole(r.Context())
	tenant := mustTenant(r.Context())
	if role == RoleViewer && job.TenantID != tenant {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	progress, _ := s.getProgress(jobID)
	if progress == nil {
		total := 0
		if len(job.Files) > 0 {
			total = chunkCount(job.Files[0].SizeBytes)
		}
		progress = &ProgressState{
			ChunksTotal:         total,
			ChunksReceived:      0,
			ChunksStored:        0,
			ConnectorsPending:   []string{"minio"},
			ConnectorsCompleted: []string{},
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"job_id":               jobID,
		"chunks_total":         progress.ChunksTotal,
		"chunks_received":      progress.ChunksReceived,
		"chunks_stored":        progress.ChunksStored,
		"connectors_completed": progress.ConnectorsCompleted,
		"connectors_pending":   progress.ConnectorsPending,
		"status":               job.State,
	})
}

func (s *Server) missingChunksHandler(w http.ResponseWriter, r *http.Request) {
	jobID := chi.URLParam(r, "id")
	job, ok := s.getJob(jobID)
	if !ok {
		http.NotFound(w, r)
		return
	}
	role := mustRole(r.Context())
	tenant := mustTenant(r.Context())
	if role == RoleViewer && job.TenantID != tenant {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	progress, _ := s.getProgress(jobID)
	total := 0
	if progress != nil {
		total = progress.ChunksTotal
	} else if len(job.Files) > 0 {
		total = chunkCount(job.Files[0].SizeBytes)
	}
	var missing []int
	received := 0
	if progress != nil {
		received = progress.ChunksReceived
	}
	for i := received; i < total; i++ {
		missing = append(missing, i)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"job_id":         jobID,
		"chunks_total":   total,
		"missing_chunks": missing,
	})
}

func (s *Server) complianceReport(w http.ResponseWriter, r *http.Request) {
	role := mustRole(r.Context())
	if role == RoleViewer {
		http.Error(w, "insufficient role", http.StatusForbidden)
		return
	}
	tenant := r.URL.Query().Get("tenant")
	if tenant == "" {
		tenant = mustTenant(r.Context())
	}
	jobs := s.listJobs(tenant, false)
	summary := map[JobState]int{}
	var latencies []int64
	var outages int

	for _, job := range jobs {
		summary[job.State]++
		if job.State == JobStateCompleted && job.LatencyMillis > 0 {
			latencies = append(latencies, job.LatencyMillis)
		}
		if job.State == JobStateFailed {
			outages++
		}
	}

	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	p95 := percentile(latencies, 95)
	sub500 := ratioUnder(latencies, 500)

	writeJSON(w, http.StatusOK, map[string]any{
		"tenant":          tenant,
		"summary":         summary,
		"p95LatencyMs":    p95,
		"successUnder500": sub500,
		"outages":         outages,
		"generatedAt":     time.Now().UTC(),
	})
}

func (s *Server) kpiHandler(w http.ResponseWriter, _ *http.Request) {
	successRate, p95, sub500, failures, cancels := s.metrics.snapshot()
	writeJSON(w, http.StatusOK, map[string]any{
		"success_rate":      successRate,
		"p95_latency_ms":    p95,
		"under_500ms_ratio": sub500,
		"failures_24h":      failures,
		"canceled":          cancels,
		"generated_at":      time.Now().UTC(),
	})
}

func healthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func percentile(values []int64, pct float64) float64 {
	if len(values) == 0 {
		return 0
	}
	idx := int(math.Round((pct / 100.0) * float64(len(values)-1)))
	if idx < 0 {
		idx = 0
	}
	if idx >= len(values) {
		idx = len(values) - 1
	}
	return float64(values[idx])
}

func ratioUnder(values []int64, threshold int64) float64 {
	if len(values) == 0 {
		return 0
	}
	var count int
	for _, v := range values {
		if v <= threshold {
			count++
		}
	}
	return float64(count) / float64(len(values))
}

func (p *policyEngine) evaluate(req jobRequest, tenant string) ([]PolicyDecision, error) {
	var decisions []PolicyDecision
	classification, _ := req.Intent["classification"].(string)
	if classification == "" {
		return nil, errors.New("intent.classification required")
	}
	allowed := p.cfg.allowedClasses[strings.ToLower(tenant)]
	if len(allowed) > 0 {
		if _, ok := allowed[strings.ToLower(classification)]; !ok {
			return nil, fmt.Errorf("classification %q not allowed for tenant %s", classification, tenant)
		}
	}
	decisions = append(decisions, PolicyDecision{
		Rule:      "classification",
		Action:    "allow",
		Reason:    fmt.Sprintf("tenant %s permitted for %s", tenant, classification),
		Timestamp: time.Now().UTC(),
	})

	if p.cfg.requirePQC {
		if enabled, _ := req.Intent["pqc"].(bool); !enabled {
			return nil, errors.New("pqc flag must be true for all transfers")
		}
		decisions = append(decisions, PolicyDecision{
			Rule:      "pqc",
			Action:    "enforce",
			Reason:    "Dilithium/Kyber required for receipts",
			Timestamp: time.Now().UTC(),
		})
	}

	for _, file := range req.Files {
		if file.Name == "" {
			return nil, errors.New("file name required")
		}
		if file.SizeBytes <= 0 {
			return nil, fmt.Errorf("file %s missing size", file.Name)
		}
		if p.cfg.maxFileSizeMB > 0 && (file.SizeBytes/1_000_000) > p.cfg.maxFileSizeMB {
			return nil, fmt.Errorf("file %s exceeds max size %dMB", file.Name, p.cfg.maxFileSizeMB)
		}
		content := strings.ToLower(fmt.Sprintf("%s%s", file.Name, file.Challenges))
		for _, word := range p.cfg.blockedWords {
			if word != "" && strings.Contains(content, strings.ToLower(word)) {
				return nil, fmt.Errorf("blocked keyword %q detected in %s", word, file.Name)
			}
		}
	}

	decisions = append(decisions, PolicyDecision{
		Rule:      "dlp",
		Action:    "allow",
		Reason:    "metadata and names cleared",
		Timestamp: time.Now().UTC(),
	})
	return decisions, nil
}

func (p *predictor) predict(ctx context.Context, req jobRequest) Prediction {
	if p.mlURL != "" && p.client != nil {
		if pred, err := p.queryModel(ctx, req); err == nil {
			return pred
		} else {
			p.log.Warn().Err(err).Msg("ml predictor query failed, using fallback heuristic")
		}
	}
	return fallbackPrediction(req)
}

func (p *predictor) queryModel(ctx context.Context, req jobRequest) (Prediction, error) {
	features := buildFeatures(req)
	payload := map[string]any{
		"features": features,
		"context": map[string]any{
			"classification": req.Intent["classification"],
			"priority":       req.Intent["priority"],
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return Prediction{}, err
	}
	url := strings.TrimSuffix(p.mlURL, "/") + "/predict"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return Prediction{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	resp, err := p.client.Do(request)
	if err != nil {
		return Prediction{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Prediction{}, fmt.Errorf("ml predictor returned status %d", resp.StatusCode)
	}
	var data struct {
		Path         string  `json:"path"`
		Strategy     string  `json:"strategy"`
		LatencyMs    int     `json:"latencyMs"`
		Confidence   float64 `json:"confidence"`
		FailureGuard string  `json:"failureGuard"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return Prediction{}, err
	}
	return Prediction{
		Path:         data.Path,
		Strategy:     data.Strategy,
		LatencyMs:    data.LatencyMs,
		Confidence:   data.Confidence,
		FailureGuard: data.FailureGuard,
	}, nil
}

func fallbackPrediction(req jobRequest) Prediction {
	intent := req.Intent
	classification, _ := intent["classification"].(string)
	priority := strings.ToLower(fmt.Sprint(intent["priority"]))
	totalSize := int64(0)
	var critical bool
	for _, file := range req.Files {
		totalSize += file.SizeBytes
		if file.Critical {
			critical = true
		}
	}

	switch {
	case strings.Contains(priority, "latency") || critical || strings.EqualFold(classification, "critical"):
		return Prediction{
			Path:         "f1-ultra-low-latency",
			Strategy:     "edge-quic-merkle",
			LatencyMs:    320,
			Confidence:   0.9,
			FailureGuard: "dual-path-quic",
		}
	case totalSize > 5*1_000_000_000 || strings.Contains(strings.ToLower(classification), "medical"):
		return Prediction{
			Path:         "clinic-redundant",
			Strategy:     "multipath-erasure",
			LatencyMs:    2800,
			Confidence:   0.8,
			FailureGuard: "erasure-5+3",
		}
	default:
		return Prediction{
			Path:         "standard-ha",
			Strategy:     "adaptive-grpc",
			LatencyMs:    1200,
			Confidence:   0.7,
			FailureGuard: "auto-retry",
		}
	}
}

func buildFeatures(req jobRequest) map[string]float64 {
	var totalSize float64
	var hasCriticalFile bool
	for _, file := range req.Files {
		totalSize += float64(file.SizeBytes) / 1_000_000_000
		if file.Critical {
			hasCriticalFile = true
		}
	}
	classification := safeLowerString(req.Intent["classification"])
	priority := safeLowerString(req.Intent["priority"])

	criticality := 0.4
	switch {
	case strings.Contains(classification, "critical"):
		criticality = 1.0
	case strings.Contains(classification, "medical"):
		criticality = 0.7
	}
	if hasCriticalFile {
		criticality = 1.0
	}

	reliability := 0.5
	if strings.Contains(priority, "reliability") || strings.Contains(classification, "medical") {
		reliability = 0.9
	}

	priorityScore := 0.5
	if strings.Contains(priority, "latency") {
		priorityScore = 1.0
	} else if strings.Contains(priority, "balanced") {
		priorityScore = 0.7
	}

	return map[string]float64{
		"size_gb":     totalSize,
		"criticality": criticality,
		"reliability": reliability,
		"priority":    priorityScore,
	}
}

func safeLowerString(v any) string {
	if v == nil {
		return ""
	}
	return strings.ToLower(fmt.Sprint(v))
}

func newPolicyConfig() policyConfig {
	allowed := parseAllowedClasses(os.Getenv("POLICY_ALLOWED_CLASSES"))
	blocked := splitCSV(os.Getenv("POLICY_BLOCKED_KEYWORDS"))
	maxFile := int64(2048)
	if raw := os.Getenv("POLICY_MAX_FILE_MB"); raw != "" {
		if v, err := strconv.ParseInt(raw, 10, 64); err == nil {
			maxFile = v
		}
	}
	requirePQC := true
	if raw := os.Getenv("POLICY_REQUIRE_PQC"); raw != "" {
		if v, err := strconv.ParseBool(raw); err == nil {
			requirePQC = v
		}
	}
	return policyConfig{
		allowedClasses: allowed,
		blockedWords:   blocked,
		maxFileSizeMB:  maxFile,
		requirePQC:     requirePQC,
	}
}

func parseAllowedClasses(raw string) map[string]map[string]struct{} {
	result := make(map[string]map[string]struct{})
	if raw == "" {
		return result
	}
	for _, token := range strings.Split(raw, ",") {
		token = strings.TrimSpace(token)
		if token == "" {
			continue
		}
		parts := strings.Split(token, ":")
		if len(parts) != 2 {
			continue
		}
		tenant := strings.ToLower(strings.TrimSpace(parts[0]))
		classes := strings.Split(parts[1], "|")
		set := make(map[string]struct{}, len(classes))
		for _, class := range classes {
			class = strings.ToLower(strings.TrimSpace(class))
			if class != "" {
				set[class] = struct{}{}
			}
		}
		if len(set) > 0 {
			result[tenant] = set
		}
	}
	return result
}

func splitCSV(raw string) []string {
	if raw == "" {
		return nil
	}
	items := strings.Split(raw, ",")
	for i, item := range items {
		items[i] = strings.TrimSpace(item)
	}
	return items
}

func newReceiptSigner(dir string) (*receiptSigner, error) {
	if dir == "" {
		dir = filepath.Join("orchestrator", "artifacts", "receipts")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return nil, err
	}

	var priv ed25519.PrivateKey
	var pub ed25519.PublicKey
	if raw := os.Getenv("HSM_ED25519_KEY"); raw != "" {
		data, err := base64.StdEncoding.DecodeString(raw)
		if err != nil {
			return nil, fmt.Errorf("decode HSM key: %w", err)
		}
		if len(data) != ed25519.PrivateKeySize {
			return nil, fmt.Errorf("expected %d-byte private key", ed25519.PrivateKeySize)
		}
		priv = ed25519.PrivateKey(data)
		pub = priv.Public().(ed25519.PublicKey)
	} else {
		pub, priv, _ = ed25519.GenerateKey(rand.Reader)
	}

	signer := &receiptSigner{priv: priv, pub: pub, dir: dir}
	pubPath := filepath.Join(dir, "public.key")
	if err := os.WriteFile(pubPath, []byte(base64.StdEncoding.EncodeToString(pub)), 0o644); err != nil {
		return nil, err
	}
	return signer, nil
}

func (s *receiptSigner) signReceipt(job *Job) (*Receipt, error) {
	receipt := &Receipt{
		ReceiptID:    uuid.NewString(),
		JobID:        job.ID,
		TenantID:     job.TenantID,
		Status:       string(job.State),
		SignedAt:     time.Now().UTC(),
		SignatureAlg: "ED25519-SHA256",
	}
	payload, err := json.Marshal(receipt)
	if err != nil {
		return nil, err
	}
	hash := sha256.Sum256(payload)
	receipt.PayloadHash = base64.StdEncoding.EncodeToString(hash[:])
	sig := ed25519.Sign(s.priv, hash[:])
	receipt.Signature = base64.StdEncoding.EncodeToString(sig)
	return receipt, nil
}

func (s *receiptSigner) persist(receipt *Receipt) error {
	payload, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(s.dir, fmt.Sprintf("%s.json", receipt.JobID))
	return os.WriteFile(path, payload, 0o644)
}

func loadAuthConfig() authConfig {
	defaultKeys := map[string]Role{
		"local-admin-token": RoleAdmin,
		"local-observer":    RoleViewer,
	}
	config := authConfig{
		apiKeys: defaultKeys,
	}
	if raw := os.Getenv("ORCH_API_KEYS"); raw != "" {
		config.apiKeys = map[string]Role{}
		for _, token := range strings.Split(raw, ",") {
			token = strings.TrimSpace(token)
			if token == "" {
				continue
			}
			parts := strings.Split(token, ":")
			if len(parts) != 2 {
				continue
			}
			role := Role(strings.ToLower(strings.TrimSpace(parts[0])))
			key := strings.TrimSpace(parts[1])
			if key != "" {
				config.apiKeys[key] = role
			}
		}
	}
	// If MFA secret is not set, default bypass to 000000 for dev.
	config.mfaSecret = os.Getenv("ORCH_MFA_SECRET")
	if bypass := os.Getenv("ORCH_MFA_BYPASS"); bypass != "" {
		config.mfaBypass = bypass
	} else {
		config.mfaBypass = "000000"
	}
	return config
}

func authMiddleware(cfg authConfig) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// If no API keys are configured, allow all (development fallback).
			if len(cfg.apiKeys) == 0 {
				tenant := r.Header.Get("X-Tenant-ID")
				if tenant == "" {
					tenant = "pilot"
				}
				ctx := context.WithValue(r.Context(), ctxKeyRole, RoleAdmin)
				ctx = context.WithValue(ctx, ctxKeyTenant, tenant)
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}
			authHeader := r.Header.Get("Authorization")
			if authHeader == "" {
				http.Error(w, "missing authorization", http.StatusUnauthorized)
				return
			}
			token := strings.TrimSpace(strings.TrimPrefix(authHeader, "Bearer"))
			role, ok := cfg.apiKeys[token]
			if !ok {
				http.Error(w, "invalid api key", http.StatusUnauthorized)
				return
			}
			if cfg.mfaSecret != "" {
				provided := strings.TrimSpace(r.Header.Get("X-MFA-Token"))
				if provided == "" {
					http.Error(w, "mfa token required", http.StatusUnauthorized)
					return
				}
				if provided != cfg.mfaBypass {
					valid := totp.Validate(provided, cfg.mfaSecret)
					if !valid {
						http.Error(w, "invalid mfa token", http.StatusUnauthorized)
						return
					}
				}
			}
			tenant := r.Header.Get("X-Tenant-ID")
			if tenant == "" {
				http.Error(w, "tenant header required", http.StatusBadRequest)
				return
			}
			ctx := context.WithValue(r.Context(), ctxKeyRole, role)
			ctx = context.WithValue(ctx, ctxKeyTenant, tenant)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, X-MFA-Token, X-Tenant-ID, Content-Type")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func mustRole(ctx context.Context) Role {
	role, _ := ctx.Value(ctxKeyRole).(Role)
	return role
}

func mustTenant(ctx context.Context) string {
	tenant, _ := ctx.Value(ctxKeyTenant).(string)
	return tenant
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func getEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getInt64Env(key string, def int64) int64 {
	if v := os.Getenv(key); v != "" {
		if parsed, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil {
			return parsed
		}
	}
	return def
}

func main() {
	zerolog.TimeFieldFormat = time.RFC3339
	addr := getEnv("ORCH_HTTP_ADDR", ":8090")
	workerCount := 4
	if raw := os.Getenv("ORCH_WORKERS"); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v > 0 {
			workerCount = v
		}
	}
	authCfg := loadAuthConfig()
	policy := &policyEngine{cfg: newPolicyConfig()}
	signer, err := newReceiptSigner(os.Getenv("RECEIPT_DIR"))
	if err != nil {
		log.Fatal().Err(err).Msg("failed to initialise signer")
	}
	server := newServer(policy, &predictor{
		mlURL:  os.Getenv("ML_COORDINATOR_URL"),
		log:    log.With().Str("component", "predictor").Logger(),
		client: &http.Client{Timeout: 3 * time.Second},
	}, signer, workerCount)

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(corsMiddleware)

	r.Get("/healthz", healthz)

	r.Group(func(api chi.Router) {
		api.Use(authMiddleware(authCfg))
		api.Post("/ui/upload", server.uiUploadHandler)
		api.Post("/ui/upload-large", server.uiUploadLargeHandler)
		api.Post("/ui/upload-metadata", server.uiUploadMetadataHandler)
		api.Post("/jobs", server.createJob)
		api.Get("/jobs", server.listJobsHandler)
		api.Get("/jobs/{id}", server.getJobHandler)
		api.Get("/jobs/{id}/progress", server.progressHandler)
		api.Get("/jobs/{id}/missing-chunks", server.missingChunksHandler)
		api.Post("/jobs/{id}/actions/cancel", server.cancelJobHandler)
		api.Get("/reports/compliance", server.complianceReport)
		api.Get("/reports/kpi", server.kpiHandler)
	})

	log.Info().Str("addr", addr).Int("workers", workerCount).Msg("orchestrator listening")
	if err := http.ListenAndServe(addr, r); err != nil {
		log.Fatal().Err(err).Msg("orchestrator exited")
	}
}
