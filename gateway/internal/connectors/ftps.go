package connectors

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/secsy/goftp"
	manifestpb "github.com/trackshift/platform/proto/trackshift/manifest"
)

type ftpsConnector struct {
	config  goftp.Config
	addr    string
	baseDir string
}

func NewFTPSConnector() (Connector, error) {
	host := os.Getenv("FTPS_HOST")
	user := os.Getenv("FTPS_USER")
	pw := os.Getenv("FTPS_PASSWORD")
	if host == "" || user == "" || pw == "" {
		return nil, fmt.Errorf("FTPS_HOST/FTPS_USER/FTPS_PASSWORD required for ftps connector")
	}
	port := os.Getenv("FTPS_PORT")
	if port == "" {
		port = "21"
	}
	if _, err := strconv.Atoi(port); err != nil {
		return nil, fmt.Errorf("invalid ftps port: %w", err)
	}
	addr := fmt.Sprintf("%s:%s", host, port)
	return &ftpsConnector{
		config: goftp.Config{
			User:               user,
			Password:           pw,
			TLSConfig:          &tls.Config{InsecureSkipVerify: true}, // rely on network ACLs for now
			TLSMode:            goftp.TLSExplicit,
			Timeout:            30 * time.Second,
			Logger:             nil,
			ConnectionsPerHost: 1,
		},
		addr:    addr,
		baseDir: os.Getenv("FTPS_BASE_DIR"),
	}, nil
}

func (f *ftpsConnector) Name() string {
	return "ftps"
}

func (f *ftpsConnector) StoreManifest(_ context.Context, manifest *manifestpb.Manifest, payload []byte) error {
	return f.write(manifest.GetJobId(), "manifest.json", payload)
}

func (f *ftpsConnector) StoreChunk(_ context.Context, jobID string, chunkIndex uint32, data []byte) error {
	name := fmt.Sprintf("chunks/chunk-%05d.bin", chunkIndex)
	return f.write(jobID, name, data)
}

func (f *ftpsConnector) write(jobID, suffix string, data []byte) error {
	client, err := goftp.DialConfig(f.config, f.addr)
	if err != nil {
		return fmt.Errorf("ftps dial: %w", err)
	}
	defer client.Close()

	targetPath := f.remotePath(jobID, suffix)
	if err := f.ensureDir(client, path.Dir(targetPath)); err != nil {
		return err
	}
	if err := client.Store(targetPath, bytes.NewReader(data)); err != nil {
		return fmt.Errorf("ftps store: %w", err)
	}
	return nil
}

func (f *ftpsConnector) remotePath(jobID, suffix string) string {
	if f.baseDir == "" {
		return path.Join(jobID, suffix)
	}
	return path.Join(f.baseDir, jobID, suffix)
}

func (f *ftpsConnector) ensureDir(client *goftp.Client, dir string) error {
	if dir == "" || dir == "." || dir == "/" {
		return nil
	}
	segments := strings.Split(dir, "/")
	current := ""
	for _, segment := range segments {
		if segment == "" {
			continue
		}
		current = path.Join(current, segment)
		if _, err := client.Mkdir(current); err != nil {
			if !strings.Contains(strings.ToLower(err.Error()), "file exists") {
				return fmt.Errorf("ftps mkdir %s: %w", current, err)
			}
		}
	}
	return nil
}
