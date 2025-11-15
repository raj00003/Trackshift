package connectors

import (
	"context"
	"fmt"
	"net"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/pkg/sftp"
	manifestpb "github.com/trackshift/platform/proto/trackshift/manifest"
	"golang.org/x/crypto/ssh"
)

type sftpConnector struct {
	addr     string
	user     string
	password string
	keyPath  string
	baseDir  string
}

func NewSFTPConnector() (Connector, error) {
	host := os.Getenv("SFTP_HOST")
	user := os.Getenv("SFTP_USER")
	if host == "" || user == "" {
		return nil, fmt.Errorf("SFTP_HOST and SFTP_USER required for sftp connector")
	}
	port := os.Getenv("SFTP_PORT")
	if port == "" {
		port = "22"
	}
	if _, err := strconv.Atoi(port); err != nil {
		return nil, fmt.Errorf("invalid sftp port: %w", err)
	}
	return &sftpConnector{
		addr:     net.JoinHostPort(host, port),
		user:     user,
		password: os.Getenv("SFTP_PASSWORD"),
		keyPath:  os.Getenv("SFTP_KEY_PATH"),
		baseDir:  os.Getenv("SFTP_BASE_DIR"),
	}, nil
}

func (s *sftpConnector) Name() string {
	return "sftp"
}

func (s *sftpConnector) StoreManifest(_ context.Context, manifest *manifestpb.Manifest, payload []byte) error {
	return s.writeRemote(payload, manifest.GetJobId(), "manifest.json")
}

func (s *sftpConnector) StoreChunk(_ context.Context, jobID string, chunkIndex uint32, data []byte) error {
	name := fmt.Sprintf("chunks/chunk-%05d.bin", chunkIndex)
	return s.writeRemote(data, jobID, name)
}

func (s *sftpConnector) writeRemote(data []byte, jobID, suffix string) error {
	client, err := s.newClient()
	if err != nil {
		return err
	}
	defer client.Close()

	remotePath := s.remotePath(jobID, suffix)
	if err := client.MkdirAll(path.Dir(remotePath)); err != nil {
		return err
	}
	f, err := client.OpenFile(remotePath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC)
	if err != nil {
		return err
	}
	defer f.Close()

	_, err = f.Write(data)
	return err
}

func (s *sftpConnector) newClient() (*sftp.Client, error) {
	auths := []ssh.AuthMethod{}
	if s.keyPath != "" {
		key, err := os.ReadFile(s.keyPath)
		if err != nil {
			return nil, fmt.Errorf("read key: %w", err)
		}
		signer, err := ssh.ParsePrivateKey(key)
		if err != nil {
			return nil, fmt.Errorf("parse key: %w", err)
		}
		auths = append(auths, ssh.PublicKeys(signer))
	}
	if s.password != "" {
		auths = append(auths, ssh.Password(s.password))
	}
	if len(auths) == 0 {
		return nil, fmt.Errorf("sftp connector requires password or key")
	}
	cfg := ssh.ClientConfig{
		User:            s.user,
		Auth:            auths,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         10 * time.Second,
	}

	conn, err := ssh.Dial("tcp", s.addr, &cfg)
	if err != nil {
		return nil, fmt.Errorf("ssh dial: %w", err)
	}
	return sftp.NewClient(conn)
}

func (s *sftpConnector) remotePath(jobID, suffix string) string {
	parts := []string{}
	if strings.TrimSpace(s.baseDir) != "" {
		parts = append(parts, strings.TrimSuffix(s.baseDir, "/"))
	}
	parts = append(parts, jobID, suffix)
	return path.Join(parts...)
}
