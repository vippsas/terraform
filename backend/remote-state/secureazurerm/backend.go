package secureazurerm

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/Azure/azure-sdk-for-go/storage"
	"github.com/Azure/go-autorest/autorest/azure"
	"github.com/hashicorp/terraform/backend"
	"github.com/hashicorp/terraform/helper/schema"
	"github.com/hashicorp/terraform/state"
	"github.com/hashicorp/terraform/state/remote"
	"github.com/hashicorp/terraform/terraform"
)

// Backend does backend.
type Backend struct {
	*schema.Backend

	// Fields used by Storage Account:
	blobClient    storage.BlobStorageClient
	containerName string
	blobName      string
	leaseID       string

	// Fields used by Key Vault:
	keyVaultName string
}

// config stores backend configuration.
type config struct {
	// Resource Group:
	ResourceGroupName string

	// Azure Storage Account:
	StorageAccountName string
	AccessKey          string

	// Azure Key Vault:
	KeyVaultName string

	// Credentials:
	Environment    string
	SubscriptionID string
	TenantID       string
}

// New creates a new backend for remote state stored in Azure storage account and key vault.
func New() backend.Backend {
	s := &schema.Backend{
		Schema: map[string]*schema.Schema{
			// Resource group:
			"resource_group_name": {
				Type:        schema.TypeString,
				Optional:    true,
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
				Optional:    true,
				Description: "The access key.",
				DefaultFunc: schema.EnvDefaultFunc("ACCESS_KEY", ""),
			},
			"container_name": {
				Type:        schema.TypeString,
				Required:    true,
				Description: "The container name.",
			},
			"blob_name": { // renamed from "key", so developers won't confuse it with keys in key vault.
				Type:        schema.TypeString,
				Required:    true,
				Description: "The blob name.",
			},

			// Azure Key Vault:
			"key_vault_name": {
				Type:        schema.TypeString,
				Required:    true,
				Description: "The key vault name.",
			},

			// Credentials:
			"environment": { // optional, automatically set to "public" if empty.
				Type:        schema.TypeString,
				Optional:    true,
				Description: "The Azure cloud environment.",
				DefaultFunc: schema.EnvDefaultFunc("ENVIRONMENT", ""),
			},
			"tenant_id": {
				Type:        schema.TypeString,
				Optional:    true,
				Description: "The tenant ID.",
				DefaultFunc: schema.EnvDefaultFunc("TENANT_ID", ""),
			},
			"subscription_id": {
				Type:        schema.TypeString,
				Optional:    true,
				Description: "The subscription ID.",
				DefaultFunc: schema.EnvDefaultFunc("SUBSCRIPTION_ID", ""),
			},
		},
	}

	result := &Backend{Backend: s}
	result.Backend.ConfigureFunc = result.configure
	return result
}

// configure bootstraps the Azure resources needed to use this backend.
func (b *Backend) configure(ctx context.Context) error {
	if b.containerName != "" {
		return nil
	}

	// Get the resource data from the backend configuration.
	data := schema.FromContextBackendConfig(ctx)
	b.containerName = data.Get("container_name").(string)
	b.blobName = data.Get("blob_name").(string)
	c := config{
		// Resource Group:
		ResourceGroupName: data.Get("resource_group_name").(string),

		// Azure Storage Account:
		StorageAccountName: data.Get("storage_account_name").(string),
		AccessKey:          data.Get("access_key").(string),

		// Azure Key Vault:
		KeyVaultName: data.Get("key_vault_name").(string),

		// Credentials:
		Environment:    data.Get("environment").(string),
		TenantID:       data.Get("tenant_id").(string),
		SubscriptionID: data.Get("subscription_id").(string),
	}

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

	client = storageClient.GetBlobService()
	return client, nil
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

const (
	// This will be used as directory name, the odd looking colon is simply to
	// reduce the chance of name conflicts with existing objects.
	keyEnvPrefix = "env:"
)

// States returns multiple remote states.
func (b *Backend) States() ([]string, error) {
	prefix := b.blobName + keyEnvPrefix
	params := storage.ListBlobsParameters{
		Prefix: prefix,
	}

	container := b.blobClient.GetContainerReference(b.containerName)
	resp, err := container.ListBlobs(params)
	if err != nil {
		return nil, err
	}

	envs := map[string]struct{}{}
	for _, obj := range resp.Blobs {
		key := obj.Name
		if strings.HasPrefix(key, prefix) {
			name := strings.TrimPrefix(key, prefix)
			// we store the state in a key, not a directory
			if strings.Contains(name, "/") {
				continue
			}
			envs[name] = struct{}{}
		}
	}

	result := []string{backend.DefaultStateName}
	for name := range envs {
		result = append(result, name)
	}
	sort.Strings(result[1:])
	return result, nil
}

// DeleteState deletes remote state.
func (b *Backend) DeleteState(name string) error {
	if name == backend.DefaultStateName || name == "" {
		return fmt.Errorf("can't delete default state")
	}

	containerReference := b.blobClient.GetContainerReference(b.containerName)
	blobReference := containerReference.GetBlobReference(b.path(name))
	options := &storage.DeleteBlobOptions{}

	return blobReference.Delete(options)
}

// State returns remote state.
func (b *Backend) State(name string) (state.State, error) {
	client := &Client{
		blobClient:    b.blobClient,
		containerName: b.containerName,
		blobName:      b.path(name),
	}

	stateMgr := &remote.State{Client: client}

	//if this isn't the default state name, we need to create the object so
	//it's listed by States.
	if name != backend.DefaultStateName {
		// take a lock on this state while we write it
		lockInfo := state.NewLockInfo()
		lockInfo.Operation = "init"
		lockID, err := client.Lock(lockInfo)
		if err != nil {
			return nil, fmt.Errorf("failed to lock azure state: %s", err)
		}

		// Local helper function so we can call it multiple places
		lockUnlock := func(parent error) error {
			if err := stateMgr.Unlock(lockID); err != nil {
				return fmt.Errorf(strings.TrimSpace(errStateUnlock), lockID, err)
			}
			return parent
		}

		// Grab the value
		if err := stateMgr.RefreshState(); err != nil {
			err = lockUnlock(err)
			return nil, err
		}

		// If we have no state, we have to create an empty state
		if v := stateMgr.State(); v == nil {
			if err := stateMgr.WriteState(terraform.NewState()); err != nil {
				err = lockUnlock(err)
				return nil, err
			}
			if err := stateMgr.PersistState(); err != nil {
				err = lockUnlock(err)
				return nil, err
			}
		}

		// Unlock, the state should now be initialized
		if err := lockUnlock(nil); err != nil {
			return nil, err
		}

	}

	return stateMgr, nil
}

func (b *Backend) client() *Client {
	return &Client{}
}

func (b *Backend) path(name string) string {
	if name == backend.DefaultStateName {
		return b.blobName
	}

	return b.blobName + keyEnvPrefix + name
}

const errStateUnlock = `
Error unlocking Azure state. Lock ID: %s

Error: %s

You may have to force-unlock this state in order to use it again.
`
