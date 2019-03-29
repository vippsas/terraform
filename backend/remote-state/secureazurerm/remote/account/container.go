package account

import (
	"context"
	"errors"
	"fmt"

	"github.com/Azure/go-autorest/autorest"

	armStorage "github.com/Azure/azure-sdk-for-go/services/storage/mgmt/2018-07-01/storage"
	"github.com/Azure/azure-sdk-for-go/storage"
)

// Container communicates to the container in the storage account in Azure.
type Container struct {
	BlobService storage.BlobStorageClient // Client to communicate with the Azure Resource Manager to operate on Azure Blob Storage Accounts.
	Name        string                    // The name of the container that contains the blob storing the remote state in JSON.
}

// Setup creates a new remote client to the storage account.
func Setup(ctx context.Context, authorizer autorest.Authorizer, subscriptionID string, resourceGroupName string, storageAccountName string, containerName string) (Container, error) {
	var c Container

	accountsClient := armStorage.NewAccountsClient(subscriptionID)
	accountsClient.Authorizer = authorizer
	/* List to check, then if not exist, create it.
	accountsClient.Create(ctx, resourceGroupName, storageAccountName, armStorage.AccountCreateParameters{
		Kind: armStorage.BlobStorage,
	})
	*/

	// Fetch access key for storage account.
	keys, err := accountsClient.ListKeys(ctx, resourceGroupName, storageAccountName)
	if err != nil {
		return c, fmt.Errorf("error listing the access keys in the storage account %q: %s", storageAccountName, err)
	}
	if keys.Keys == nil {
		return c, fmt.Errorf("no keys returned from storage account %q", storageAccountName)
	}
	accessKey1 := *(*keys.Keys)[0].Value
	if accessKey1 == "" {
		return c, errors.New("missing access key")
	}

	// Create new storage account client using fetched access key.
	storageClient, err := storage.NewBasicClient(storageAccountName, accessKey1)
	if err != nil {
		return c, fmt.Errorf("error creating client for storage account %q: %s", storageAccountName, err)
	}

	// Check if the given container exists.
	blobService := storageClient.GetBlobService()
	c.Name = containerName
	resp, err := blobService.ListContainers(storage.ListContainersParameters{Prefix: c.Name, MaxResults: 1})
	if err != nil {
		return c, fmt.Errorf("error listing containers: %s", err)
	}
	for _, container := range resp.Containers {
		if container.Name == c.Name {
			c.BlobService = blobService
			return c, nil // success!
		}
	}
	// TODO: Create container if it does not exists.
	return c, fmt.Errorf("cannot find container: %s", c.Name)
}

// List lists blobs in the container.
func (c *Container) List() ([]storage.Blob, error) {
	r, err := c.BlobService.GetContainerReference(c.Name).ListBlobs(storage.ListBlobsParameters{})
	if err != nil {
		return nil, err
	}
	return r.Blobs, nil
}

// GetBlobRef returns the blob reference to the client's blob.
func (c *Container) GetBlobRef(blobName string) *storage.Blob {
	return c.BlobService.GetContainerReference(c.Name).GetBlobReference(blobName)
}
