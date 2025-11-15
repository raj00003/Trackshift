package dlp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	manifestpb "github.com/trackshift/platform/proto/trackshift/manifest"
)

// Violation describes a DLP/AV policy failure.
type Violation struct {
	Rule   string
	Detail string
}

func (v *Violation) Error() string {
	return fmt.Sprintf("dlp violation (%s): %s", v.Rule, v.Detail)
}

// Scanner executes policy checks on manifests and chunks.
type Scanner interface {
	ScanManifest(ctx context.Context, manifest *manifestpb.Manifest) error
	ScanChunk(ctx context.Context, jobID string, data []byte) error
	Enforced() bool
}

// RuleScanner performs simple extension/metadata/AV signature checks.
type RuleScanner struct {
	blockedExt        map[string]struct{}
	blockedMetadata   map[string]struct{}
	maxFileSize       uint64
	avSignatures      [][]byte
	enforceViolations bool
}

// NewRuleScannerFromEnv builds a scanner from environment variables.
// It can be disabled entirely via DLP_DISABLED=true.
func NewRuleScannerFromEnv() Scanner {
	if strings.EqualFold(os.Getenv("DLP_DISABLED"), "true") {
		return nil
	}

	s := &RuleScanner{
		blockedExt: map[string]struct{}{
			".exe": {},
			".bat": {},
			".ps1": {},
			".js":  {},
		},
		blockedMetadata:   make(map[string]struct{}),
		maxFileSize:       0,
		enforceViolations: !strings.EqualFold(os.Getenv("DLP_MODE"), "monitor"),
	}

	if raw := os.Getenv("DLP_BLOCKED_EXTENSIONS"); raw != "" {
		s.blockedExt = make(map[string]struct{})
		for _, ext := range strings.Split(raw, ",") {
			ext = strings.ToLower(strings.TrimSpace(ext))
			if ext == "" {
				continue
			}
			if !strings.HasPrefix(ext, ".") {
				ext = "." + ext
			}
			s.blockedExt[ext] = struct{}{}
		}
	}

	if raw := os.Getenv("DLP_BLOCKED_METADATA_KEYS"); raw != "" {
		for _, k := range strings.Split(raw, ",") {
			k = strings.ToLower(strings.TrimSpace(k))
			if k != "" {
				s.blockedMetadata[k] = struct{}{}
			}
		}
	}

	if raw := os.Getenv("DLP_MAX_FILE_SIZE"); raw != "" {
		if v, err := strconv.ParseUint(raw, 10, 64); err == nil {
			s.maxFileSize = v
		}
	}

	if raw := os.Getenv("DLP_AV_PATTERNS"); raw != "" {
		for _, pat := range strings.Split(raw, ",") {
			if trimmed := strings.TrimSpace(pat); trimmed != "" {
				s.avSignatures = append(s.avSignatures, []byte(trimmed))
			}
		}
	}

	// Keep scanner allocated even if no explicit rules were provided so we can
	// still block default executable extensions.
	return s
}

func (s *RuleScanner) Enforced() bool {
	return s.enforceViolations
}

func (s *RuleScanner) ScanManifest(_ context.Context, manifest *manifestpb.Manifest) error {
	if manifest == nil {
		return errors.New("manifest required")
	}
	if manifest.GetFileName() != "" {
		ext := strings.ToLower(filepath.Ext(manifest.GetFileName()))
		if _, blocked := s.blockedExt[ext]; blocked {
			return &Violation{
				Rule:   "blocked_extension",
				Detail: fmt.Sprintf("extension %q not allowed", ext),
			}
		}
	}
	if s.maxFileSize > 0 && manifest.GetFileSize() > s.maxFileSize {
		return &Violation{
			Rule:   "max_file_size",
			Detail: fmt.Sprintf("file size %d exceeds limit %d", manifest.GetFileSize(), s.maxFileSize),
		}
	}
	if len(s.blockedMetadata) > 0 {
		for _, meta := range manifest.GetMetadata() {
			key := strings.ToLower(meta.GetKey())
			if _, blocked := s.blockedMetadata[key]; blocked {
				return &Violation{
					Rule:   "blocked_metadata",
					Detail: fmt.Sprintf("metadata key %q is restricted", meta.GetKey()),
				}
			}
		}
	}
	return nil
}

func (s *RuleScanner) ScanChunk(_ context.Context, jobID string, data []byte) error {
	if len(s.avSignatures) == 0 || len(data) == 0 {
		return nil
	}
	for _, sig := range s.avSignatures {
		if len(sig) == 0 {
			continue
		}
		if bytes.Contains(data, sig) {
			return &Violation{
				Rule:   "av_signature",
				Detail: fmt.Sprintf("job %s matched AV signature", jobID),
			}
		}
	}
	return nil
}
