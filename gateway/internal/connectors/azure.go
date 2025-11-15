package connectors

import (
	"context"
	"fmt"
	"os"
	"path"

	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	manifestpb "github.com/trackshift/platform/proto/trackshift/manifest"
)

type azureConnector struct {
	client    *azblob.Client
	container string
	prefix    string
}

func NewAzureBlobConnector(ctx context.Context) (Connector, error) {
	account := os.Getenv("AZURE_STORAGE_ACCOUNT")
	key := os.Getenv("AZURE_STORAGE_KEY")
	container := os.Getenv("AZURE_BLOB_CONTAINER")
	if account == "" || key == "" || container == "" {
		return nil, fmt.Errorf("AZURE_STORAGE_ACCOUNT/AZURE_STORAGE_KEY/AZURE_BLOB_CONTAINER required for azure connector")
	}
	prefix := os.Getenv("AZURE_BLOB_PREFIX")
	credential, err := azblob.NewSharedKeyCredential(account, key)
	if err != nil {
		return nil, fmt.Errorf("build shared key credential: %w", err)
	}
	url := fmt.Sprintf("https://%s.blob.core.windows.net/", account)
	client, err := azblob.NewClientWithSharedKeyCredential(url, credential, nil)
	if err != nil {
		return nil, fmt.Errorf("create blob client: %w", err)
	}
	return &azureConnector{
		client:    client,
		container: container,
		prefix:    prefix,
	}, nil
}

func (a *azureConnector) Name() string {
	return "azure"
}

func (a *azureConnector) StoreManifest(ctx context.Context, manifest *manifestpb.Manifest, payload []byte) error {
	blobName := a.keyFor(manifest.GetJobId(), "manifest.json")
	_, err := a.client.UploadBuffer(ctx, a.container, blobName, payload, nil)
	return err
}

func (a *azureConnector) StoreChunk(ctx context.Context, jobID string, chunkIndex uint32, data []byte) error {
	blobName := a.keyFor(jobID, fmt.Sprintf("chunks/chunk-%05d.bin", chunkIndex))
	_, err := a.client.UploadBuffer(ctx, a.container, blobName, data, nil)
	return err
}

func (a *azureConnector) keyFor(jobID, suffix string) string {
	if a.prefix == "" {
		return path.Join(jobID, suffix)
	}
	return path.Join(a.prefix, jobID, suffix)
}
