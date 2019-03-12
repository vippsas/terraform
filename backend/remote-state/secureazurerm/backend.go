package secureazurerm

import (
	"context"
	"fmt"
	"sort"

	"github.com/Azure/azure-sdk-for-go/storage"
	"github.com/Azure/go-autorest/autorest/azure"
	"github.com/hashicorp/terraform/backend"
	"github.com/hashicorp/terraform/helper/schema"
	"github.com/hashicorp/terraform/state"
	"github.com/hashicorp/terraform/state/remote"
	"github.com/hashicorp/terraform/terraform"
)

// Backend maintains the remote state in Azure.
type Backend struct {
	*schema.Backend

	// Fields used by Storage Account:
	blobClient    storage.BlobStorageClient
	containerName string
	blobName      string
	leaseID       string
}

// config stores backend configuration.
type config struct {
	// Resource Group:
	ResourceGroupName string

	// Azure Storage Account:
	StorageAccountName string
	ContainerName      string
	AccessKey          string
}

// New creates a new backend for remote state stored in Azure storage account and key vault.
func New() backend.Backend {
	s := &schema.Backend{
		// Fields in backend {}. Ensure that all values are stored only in the configuration files.
		Schema: map[string]*schema.Schema{
			// Resource group:
			"resource_group_name": {
				Type:        schema.TypeString,
				Required:    true,
				Description: "The resource group name.",
			},

			// Azure Storage Account:
			"storage_account_name": {
				Type:        schema.TypeString,
				Required:    true,
				Description: "The name of the storage account.",
			},
			"access_key": { // storage account access key.
				Type:        schema.TypeString,
				Required:    true,
				Description: "The access key.",
			},
			"container_name": {
				Type:        schema.TypeString,
				Required:    true,
				Description: "The container name.",
			},
		},
	}
	b := &Backend{Backend: s}
	b.Backend.ConfigureFunc = b.configure
	return b
}

// configure bootstraps the Azure resources needed to use this backend.
func (b *Backend) configure(ctx context.Context) error {
	// Get the data fields from the "backend"-block.
	data := schema.FromContextBackendConfig(ctx)
	b.containerName = data.Get("container_name").(string)
	c := config{
		// Resource Group:
		ResourceGroupName: data.Get("resource_group_name").(string),

		// Azure Storage Account:
		StorageAccountName: data.Get("storage_account_name").(string),
		AccessKey:          data.Get("access_key").(string),
		ContainerName:      data.Get("container_name").(string),

		// TODO: Use MSI.
	}

	// TODO:
	// 1. Check if the given resource group exists.
	//   - If not, create it!
	// 2. Check if the necessary Azure resources has been made in the resource group.
	//   - If not, provision it!

	env := azure.PublicCloud // currently only supports AzurePublicCloud.

	if c.AccessKey == "" {
		return fmt.Errorf("access key not provided")
	}

	// Create new storage account client.
	storageClient, err := storage.NewClient(c.StorageAccountName, c.AccessKey, env.StorageEndpointSuffix, storage.DefaultAPIVersion, true)
	if err != nil {
		return fmt.Errorf("error creating client for storage account %q: %s", c.StorageAccountName, err)
	}

	// Check if the given container exists.
	blobService := storageClient.GetBlobService()
	resp, err := blobService.ListContainers(storage.ListContainersParameters{Prefix: c.ContainerName, MaxResults: 1})
	if err != nil {
		return fmt.Errorf("error listing containers: %s", err)
	}
	for _, container := range resp.Containers {
		if container.Name == c.ContainerName {
			b.blobClient = blobService
			return nil // success!
		}
	}
	return fmt.Errorf("cannot find container: %s", c.ContainerName)
}

/*
// getAccessKey gets the access key needed to access the storage account that stores the remote state.
func getAccessKey(c config, env azure.Environment) (string, error) {
	if c.AccessKey != "" {
		return c.AccessKey, nil
	}

		if c.ResourceGroupName != "" || c.SubscriptionID != "" || c.TenantID != "" {
			return "", fmt.Errorf("resource_group_name and credentials must be provided when access_key is absent")
		}

		oauthConfig, err := adal.NewOAuthConfig(env.ActiveDirectoryEndpoint, c.TenantID)
		if err != nil {
			return "", err
		}

		spt, err := adal.NewServicePrincipalToken(*oauthConfig, c.ClientID, c.ClientSecret, env.ResourceManagerEndpoint)
		if err != nil {
			return "", err
		}

		accountsClient := armStorage.NewAccountsClientWithBaseURI(env.ResourceManagerEndpoint, c.SubscriptionID)
		accountsClient.Authorizer = autorest.NewBearerAuthorizer(spt)

		keys, err := accountsClient.ListKeys(c.ResourceGroupName, c.StorageAccountName)
		if err != nil {
			return "", fmt.Errorf("error retrieving keys for storage account %q: %s", c.StorageAccountName, err)
		}

		if keys.Keys == nil {
			return "", fmt.Errorf("nil key returned for storage account %q", c.StorageAccountName)
		}

		accessKeys := *keys.Keys
		return *accessKeys[0].Value, nil
	return "", fmt.Errorf("access key not provided")
}
*/

// States returns a list of the names of all remote states stored in separate unique blob.
// They are all named after the workspace.
// Basically, remote state = workspace = blob.
func (b *Backend) States() ([]string, error) {
	// Get blobs of container.
	r, err := b.blobClient.GetContainerReference(b.containerName).ListBlobs(storage.ListBlobsParameters{})
	if err != nil {
		return nil, err
	}

	// List workspaces (which is equivalent to blobs) in the container.
	workspaces := []string{}
	for _, blob := range r.Blobs {
		workspaces = append(workspaces, blob.Name)
	}
	sort.Strings(workspaces[1:]) // default is placed first in the returned list.
	return workspaces, nil
}

// DeleteState deletes remote state.
func (b *Backend) DeleteState(name string) error {
	if name == backend.DefaultStateName {
		return fmt.Errorf("can't delete default state")
	}
	c := &Client{
		blobClient:    b.blobClient,
		containerName: b.containerName,
		blobName:      name, // workspace name.
	}
	lockInfo := state.NewLockInfo()
	lockInfo.Operation = "init"
	leaseID, err := c.Lock(lockInfo)
	if err != nil {
		return fmt.Errorf("error locking blob: %s", err)
	}
	if err = c.Delete(); err != nil {
		if err := c.Unlock(leaseID); err != nil {
			return fmt.Errorf("error unlocking blob (may need to be manually broken): %s", err)
		}
		return fmt.Errorf("error deleting blob: %s", err)
	}
	return nil
}

// State returns remote state specified by name.
func (b *Backend) State(name string) (state.State, error) {
	c := &Client{
		blobClient:    b.blobClient,
		containerName: b.containerName,
		blobName:      name, // workspace name.
	}
	s := &remote.State{Client: c}

	// Check if blob exists.
	exists, err := c.Exists()
	if err != nil {
		return nil, err // failed to check blob existence.
	}
	// If not exists, write empty state blob (no need for lock when the blob does not exists).
	if !exists {
		// Create new state in-memory.
		if err := s.WriteState(terraform.NewState()); err != nil {
			return nil, err
		}
		// Write that in-memory state to remote state.
		if err := s.PersistState(); err != nil {
			return nil, err
		}
	}

	return s, nil
}
