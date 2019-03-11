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
	AccessKey          string
	ContainerName      string

	// Credentials:
	Environment    string
	SubscriptionID string
	TenantID       string
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

			// Credentials:
			"environment": { // optional, automatically set to "public" if empty.
				Type:        schema.TypeString,
				Required:    true,
				Description: "The Azure cloud environment.",
			},
			"tenant_id": {
				Type:        schema.TypeString,
				Required:    true,
				Description: "The tenant ID.",
			},
			"subscription_id": {
				Type:        schema.TypeString,
				Required:    true, // ensure that you don't accidently write to the wrong subscription incorrectly set by 'az'.
				Description: "The subscription ID.",
			},
		},
	}

	b := &Backend{Backend: s}
	b.Backend.ConfigureFunc = b.configure
	return b
}

// configure bootstraps the Azure resources needed to use this backend.
func (b *Backend) configure(ctx context.Context) error {
	// TODO: Check for right tenant-id and subscription.

	// TODO: Replace with panic()?
	if b.containerName != "" {
		return nil
	}

	// Get the resource data from the backend configuration.
	data := schema.FromContextBackendConfig(ctx)
	b.containerName = data.Get("container_name").(string)
	c := config{
		// Resource Group:
		ResourceGroupName: data.Get("resource_group_name").(string),

		// Azure Storage Account:
		StorageAccountName: data.Get("storage_account_name").(string),
		AccessKey:          data.Get("access_key").(string),
		ContainerName:      data.Get("container_name").(string),

		// Credentials:
		Environment:    data.Get("environment").(string),
		TenantID:       data.Get("tenant_id").(string),
		SubscriptionID: data.Get("subscription_id").(string),

		// TODO: Use MSI.
	}

	// TODO:
	// 1. Check if the given resource group exists.
	//   - If not, create it!
	// 2. Check if the necessary Azure resources has been made in the resource group.
	//   - If not, provision it!

	blobClient, err := getBlobClient(c)
	if err != nil {
		return err
	}
	b.blobClient = blobClient

	return nil
}

func getBlobClient(c config) (storage.BlobStorageClient, error) {
	var client storage.BlobStorageClient

	env, err := getAzureEnvironment(c.Environment)
	if err != nil {
		return client, err
	}

	accessKey, err := getAccessKey(c, env)
	if err != nil {
		return client, err
	}

	storageClient, err := storage.NewClient(c.StorageAccountName, accessKey, env.StorageEndpointSuffix, storage.DefaultAPIVersion, true)
	if err != nil {
		return client, fmt.Errorf("error creating storage client for storage account %q: %s", c.StorageAccountName, err)
	}

	// Check if the given container exists.
	blobService := storageClient.GetBlobService()
	resp, err := blobService.ListContainers(storage.ListContainersParameters{Prefix: c.ContainerName, MaxResults: 1})
	if err != nil {
		return client, fmt.Errorf("failed to list containers")
	}
	for _, container := range resp.Containers {
		if container.Name == c.ContainerName {
			return blobService, nil
		}
	}
	return client, fmt.Errorf("cannot find container: %s", c.ContainerName)
}

// getAccessKey gets the access key needed to access the storage account that stores the remote state.
func getAccessKey(c config, env azure.Environment) (string, error) {
	if c.AccessKey != "" {
		return c.AccessKey, nil
	}

	/*
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
	*/
	return "", fmt.Errorf("access key not provided")
}

func getAzureEnvironment(environment string) (azure.Environment, error) {
	// If "environment" was not provided, use "public" by default.
	if environment == "" {
		return azure.PublicCloud, nil
	}

	env, err := azure.EnvironmentFromName(environment)
	if err != nil {
		// try again with wrapped value to support readable values like german instead of AZUREGERMANCLOUD
		var innerErr error
		env, innerErr = azure.EnvironmentFromName(fmt.Sprintf("AZURE%sCLOUD", environment))
		if innerErr != nil {
			return env, fmt.Errorf("invalid 'environment' configuration: %s", err)
		}
	}
	return env, nil
}

// States returns a list of the names of all remote states stored in separate unique blob.
// They are all named after the workspace.
// Basically, remote state = workspace = blob.
func (b *Backend) States() ([]string, error) {
	// Get blobs of container.
	resp, err := b.blobClient.GetContainerReference(b.containerName).ListBlobs(storage.ListBlobsParameters{})
	if err != nil {
		return nil, err
	}

	// List workspaces (which is equivalent to blobs) in the container.
	workspaces := []string{}
	for _, blob := range resp.Blobs {
		workspaces = append(workspaces, blob.Name)
	}
	sort.Strings(workspaces[1:]) // default is placed first.
	return workspaces, nil
}

// DeleteState deletes remote state.
func (b *Backend) DeleteState(name string) error {
	if name == backend.DefaultStateName || name == "" {
		return fmt.Errorf("can't delete default state")
	}
	return b.blobClient.GetContainerReference(b.containerName).GetBlobReference(name).Delete(&storage.DeleteBlobOptions{})
}

// State returns remote state specified by name.
func (b *Backend) State(name string) (state.State, error) {
	c := &Client{
		blobClient:    b.blobClient,
		containerName: b.containerName,
		blobName:      name,
	}

	remoteState := &remote.State{Client: c}

	// TODO: Check if blob exists. If not, create it.

	// If this isn't the default state name, we need to create the object so it's listed by States.
	if name != backend.DefaultStateName {
		// Lock state while we write to it.
		lockInfo := state.NewLockInfo()
		lockInfo.Operation = "init"
		lockID, err := c.Lock(lockInfo)
		if err != nil {
			return nil, fmt.Errorf("failed to lock remote state: %s", err)
		}

		// Create reusable lambda to unlock mutex on remote state.
		unlock := func(parent error) error {
			if err := remoteState.Unlock(lockID); err != nil {
				return fmt.Errorf("%s (break lease on lock ID %s in order to use it again)", err, lockID)
			}
			return parent
		}

		// Grab the remote state from the specified storage account.
		if err := remoteState.RefreshState(); err != nil {
			return nil, unlock(err)
		}

		// If there is no existing remote state, create an empty state.
		if state := remoteState.State(); state == nil {
			b.blobName = name
			if err := remoteState.WriteState(terraform.NewState()); err != nil {
				return nil, unlock(err)
			}
			if err := remoteState.PersistState(); err != nil {
				return nil, unlock(err)
			}
		}

		// Unlock, the state should now be initialized.
		if err := unlock(nil); err != nil {
			return nil, err
		}
	}

	return remoteState, nil
}
