package account

import (
	"context"
	"errors"
	"fmt"
	"net/url"

	"github.com/Azure/go-autorest/autorest/to"
	"github.com/hashicorp/terraform/backend/remote-state/secureazurerm/properties"

	armStorage "github.com/Azure/azure-sdk-for-go/services/storage/mgmt/2018-07-01/storage"
	"github.com/Azure/azure-sdk-for-go/storage"
	"github.com/Azure/azure-storage-blob-go/azblob"
)

// Container communicates to the container in the storage account in Azure.
type Container struct {
	BlobService storage.BlobStorageClient // Client to communicate with the Azure Resource Manager to operate on Azure Blob Storage Accounts.
	Name        string                    // The name of the container that contains the blob storing the remote state in JSON.
}

// Setup creates a new remote client to the storage account.
func Setup(ctx context.Context, props *properties.Properties, containerName string) (*Container, error) {
	var c Container

	accountsClient := armStorage.NewAccountsClient(props.SubscriptionID)
	accountsClient.Authorizer = props.MgmtAuthorizer

	// List to check for existing storage accounts.
	result, err := accountsClient.ListByResourceGroup(ctx, props.Name)
	if err != nil {
		return nil, fmt.Errorf("error listing storage accounts by resource group %s: %s", props.Name, err)
	}

	var storageAccountName string
	// Check if none exists. If none, create one.
	if len(*result.Value) == 0 {
		storageAccountName = props.Name

		// Check if storage account name is available:
		result, err := accountsClient.CheckNameAvailability(
			ctx,
			armStorage.AccountCheckNameAvailabilityParameters{
				Name: to.StringPtr(storageAccountName),
				Type: to.StringPtr("Microsoft.Storage/storageAccounts"),
			})
		if err != nil {
			return nil, fmt.Errorf("error checking available storage account names: %v", err)
		}
		if *result.NameAvailable != true {
			return nil, fmt.Errorf("storage account name %s not available: %v", storageAccountName, err)
		}

		// Create a new storage account, since we have none.
		// TODO: Setup soft delete.
		httpsTrafficOnly := true
		future, err := accountsClient.Create(
			ctx,
			props.Name,
			storageAccountName,
			armStorage.AccountCreateParameters{
				Sku: &armStorage.Sku{
					Name: armStorage.StandardLRS,
				},
				Kind:     armStorage.BlobStorage,
				Location: to.StringPtr(props.Location),
				AccountPropertiesCreateParameters: &armStorage.AccountPropertiesCreateParameters{
					AccessTier:             armStorage.Hot,
					EnableHTTPSTrafficOnly: &httpsTrafficOnly,
				},
			})

		if err != nil {
			return nil, fmt.Errorf("failed to start creating storage account: %v", err)
		}

		if future.WaitForCompletionRef(ctx, accountsClient.Client); err != nil {
			return nil, fmt.Errorf("failed to finish creating storage account: %v", err)
		}

		// Wait for creation completion.
		storageAccount, err := future.Result(accountsClient)
		if err != nil {
			return nil, fmt.Errorf("error waiting for storage account creation: %v", err)
		}
		props.StorageAccountResourceID = *storageAccount.ID
	} else if len(*result.Value) != 1 {
		return nil, fmt.Errorf("only 1 storage account is allowed in the resource group %s", props.Name)
	} else {
		storageAccountName = *((*result.Value)[0]).Name
		props.StorageAccountResourceID = *((*result.Value)[0]).ID
	}

	// Fetch an access key for storage account.
	keys, err := accountsClient.ListKeys(ctx, props.Name, storageAccountName)
	if err != nil {
		return nil, fmt.Errorf("error listing the access keys in the storage account %q: %s", storageAccountName, err)
	}
	if keys.Keys == nil {
		return nil, fmt.Errorf("no keys returned from storage account %q", storageAccountName)
	}
	accessKey1 := *(*keys.Keys)[0].Value
	if accessKey1 == "" {
		return nil, errors.New("missing access key")
	}

	// Create new storage account client using fetched access key.
	storageClient, err := storage.NewBasicClient(storageAccountName, accessKey1)
	if err != nil {
		return nil, fmt.Errorf("error creating client for storage account %q: %s", storageAccountName, err)
	}

	// Check if the given container exists.
	blobService := storageClient.GetBlobService()
	c.Name = containerName
	resp, err := blobService.ListContainers(storage.ListContainersParameters{Prefix: c.Name, MaxResults: 1})
	if err != nil {
		return nil, fmt.Errorf("error listing containers: %s", err)
	}
	for _, container := range resp.Containers {
		// Did we find the container?
		if container.Name == c.Name {
			c.BlobService = blobService
			return &c, nil // success!
		}
	}

	// Create a new container in the storage account.
	skc, _ := azblob.NewSharedKeyCredential(storageAccountName, accessKey1)
	u, _ := url.Parse(fmt.Sprintf("https://%s.blob.core.windows.net", storageAccountName))
	if _, err = azblob.NewServiceURL(*u, azblob.NewPipeline(skc, azblob.PipelineOptions{})).NewContainerURL(containerName).Create(
		ctx,
		azblob.Metadata{},
		azblob.PublicAccessNone,
	); err != nil {
		return nil, fmt.Errorf("error creating container %s: %s", containerName, err)
	}
	c.BlobService = blobService
	return &c, nil
}

// List lists blobs in the container.
func (c *Container) List() ([]storage.Blob, error) {
	r, err := c.BlobService.GetContainerReference(c.Name).ListBlobs(storage.ListBlobsParameters{})
	if err != nil {
		return nil, err
	}
	return r.Blobs, nil
}

// GetBlob returns the blob reference to the client's blob.
func (c *Container) GetBlob(blobName string) *storage.Blob {
	return c.BlobService.GetContainerReference(c.Name).GetBlobReference(blobName)
}
