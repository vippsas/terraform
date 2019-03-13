package secureazurerm

import (
	"context"
	"fmt"
	"sort"

	armStorage "github.com/Azure/azure-sdk-for-go/services/storage/mgmt/2018-07-01/storage"
	"github.com/Azure/azure-sdk-for-go/storage"
	"github.com/Azure/go-autorest/autorest/azure/auth"
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
				Description: "The storage account name.",
			},
			"container_name": {
				Type:        schema.TypeString,
				Required:    true,
				Description: "The container name.",
			},
			"subscription_id": {
				Type:        schema.TypeString,
				Required:    true,
				Description: "The subscription ID.",
				DefaultFunc: schema.EnvDefaultFunc(auth.SubscriptionID, ""),
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

	// Resource Group:
	//resourceGroupName := data.Get("resource_group_name").(string)

	// Azure Storage Account:
	resourceGroupName := data.Get("resource_group_name").(string)
	storageAccountName := data.Get("storage_account_name").(string)
	b.containerName = data.Get("container_name").(string)
	subscriptionID := data.Get("subscription_id").(string)

	// TODO:
	// 1. Check if the given resource group exists.
	//   - If not, create it!
	// 2. Check if the necessary Azure resources has been made in the resource group.
	//   - If not, provision it!

	if subscriptionID == "" {
		return fmt.Errorf("missing subscription_id in backend-block in config file")
	}

	accountsClient := armStorage.NewAccountsClient(subscriptionID)
	authorizer, err := auth.NewAuthorizerFromCLI()
	if err != nil {
		var innerErr error
		authorizer, innerErr = auth.NewAuthorizerFromEnvironment()
		if innerErr != nil {
			return fmt.Errorf("error creating authorizer from CLI: %s: error creating authorizer from environment: %s", err, innerErr)
		}
	}
	accountsClient.Authorizer = authorizer

	keys, err := accountsClient.ListKeys(ctx, resourceGroupName, storageAccountName)
	if err != nil {
		return fmt.Errorf("error listing the access keys in the storage account %q: %s", storageAccountName, err)
	}

	if keys.Keys == nil {
		return fmt.Errorf("no keys returned from storage account %q", storageAccountName)
	}

	accessKey1 := *(*keys.Keys)[0].Value
	if accessKey1 == "" {
		return fmt.Errorf("missing access key")
	}

	// Create new storage account client.
	storageClient, err := storage.NewBasicClient(storageAccountName, accessKey1)
	if err != nil {
		return fmt.Errorf("error creating client for storage account %q: %s", storageAccountName, err)
	}

	// Check if the given container exists.
	blobService := storageClient.GetBlobService()
	resp, err := blobService.ListContainers(storage.ListContainersParameters{Prefix: b.containerName, MaxResults: 1})
	if err != nil {
		return fmt.Errorf("error listing containers: %s", err)
	}
	for _, container := range resp.Containers {
		if container.Name == b.containerName {
			b.blobClient = blobService
			return nil // success!
		}
	}
	return fmt.Errorf("cannot find container: %s", b.containerName)
}

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
		return nil, fmt.Errorf("error checking blob existence: %s", err)
	}
	// If not exists, write empty state blob (no need for lock when the blob does not exists).
	if !exists {
		// Create new state in-memory.
		if err := s.WriteState(terraform.NewState()); err != nil {
			return nil, fmt.Errorf("error creating new state in-memory: %s", err)
		}
		// Write that in-memory state to remote state.
		if err := s.PersistState(); err != nil {
			return nil, fmt.Errorf("error writing in-memory state to remote: %s", err)
		}
	}

	return s, nil
}
