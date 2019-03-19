package secureazurerm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"sync"

	armStorage "github.com/Azure/azure-sdk-for-go/services/storage/mgmt/2018-07-01/storage"
	"github.com/Azure/azure-sdk-for-go/storage"
	"github.com/Azure/go-autorest/autorest/azure/auth"
	"github.com/hashicorp/terraform/backend"
	"github.com/hashicorp/terraform/helper/schema"
)

// Backend maintains the remote state in Azure.
// TODO: Store the backend-configuration in a (separate) container instead of .terraform-dir?
type Backend struct {
	schema.Backend
	CLI CLI
	mu  sync.Mutex

	// Fields used by Storage Account:
	blobClient    storage.BlobStorageClient
	containerName string
	blobName      string
	leaseID       string
}

// New creates a new backend for remote state stored in Azure storage account and key vault.
func New() backend.Backend {
	b := &Backend{
		Backend: schema.Backend{
			// Fields in backend {}. Ensure that all values are stored only in the configuration files.
			Schema: map[string]*schema.Schema{
				// Resource group:
				"resource_group_name": {
					Type:        schema.TypeString,
					Required:    true,
					Description: "The resource group name.",
				},

				// Key Vault:
				"key_vault_name": {
					Type:        schema.TypeString,
					Required:    true,
					Description: "The key vault name.",
				},

				// Storage Account:
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
			},
		},
	}
	b.Backend.ConfigureFunc = b.configure
	return b
}

// configure bootstraps the Azure resources needed to use this backend.
func (b *Backend) configure(ctx context.Context) error {
	// Get the data attributes from the "backend"-block.
	dataAttrs := schema.FromContextBackendConfig(ctx)

	// Resource Group:
	resourceGroupName := dataAttrs.Get("resource_group_name").(string)
	fmt.Printf("TODO: Provision resource group: %s\n", resourceGroupName)
	// 1. Check if the given resource group exists.
	//   - If not, create it!
	// (idempotent)

	// Azure Key Vault:
	keyVaultName := dataAttrs.Get("key_vault_name").(string)
	fmt.Printf("TODO: Provision key vault: %s\n", keyVaultName)
	// 2. Check if the key vault has been made in the resource group.
	//   - If not, create it!
	// (idempotent)

	// Azure Storage Account:
	storageAccountName := dataAttrs.Get("storage_account_name").(string)
	// 2. Check if the storage account has been made in the resource group.
	//   - If not, create it!
	// (idempotent)

	var subscriptionID string
	// Try authorizing using Azure CLI.
	authorizer, err := auth.NewAuthorizerFromCLI()
	if err != nil {
		// Fetch subscriptionID from environment variable AZURE_SUBSCRIPTION_ID.
		settings, err := auth.GetSettingsFromEnvironment()
		if err != nil {
			return fmt.Errorf("error getting settings from environment: %s", err)
		}
		subscriptionID = settings.GetSubscriptionID()
		if subscriptionID == "" {
			return fmt.Errorf("environment variable %s is not set", auth.SubscriptionID)
		}
		// Authorize using MSI.
		var innerErr error
		authorizer, innerErr = settings.GetMSI().Authorizer()
		if innerErr != nil {
			return fmt.Errorf("error creating authorizer from CLI: %s: error creating authorizer from environment: %s", err, innerErr)
		}
	} else {
		// Fetch subscriptionID from Azure CLI.
		out, err := exec.Command("az", "account", "show", "--output", "json", "--query", "id").Output()
		if err != nil {
			return fmt.Errorf("error fetching subscription id using Azure CLI: %s", err)
		}
		if err = json.Unmarshal(out, &subscriptionID); err != nil {
			return fmt.Errorf("error unmarshalling JSON output from Azure CLI: %s", err)
		}
	}
	accountsClient := armStorage.NewAccountsClient(subscriptionID)
	accountsClient.Authorizer = authorizer

	// Fetch access key for storage account.
	keys, err := accountsClient.ListKeys(ctx, resourceGroupName, storageAccountName)
	if err != nil {
		return fmt.Errorf("error listing the access keys in the storage account %q: %s", storageAccountName, err)
	}
	if keys.Keys == nil {
		return fmt.Errorf("no keys returned from storage account %q", storageAccountName)
	}
	accessKey1 := *(*keys.Keys)[0].Value
	if accessKey1 == "" {
		return errors.New("missing access key")
	}

	// Create new storage account client using fetched access key.
	storageClient, err := storage.NewBasicClient(storageAccountName, accessKey1)
	if err != nil {
		return fmt.Errorf("error creating client for storage account %q: %s", storageAccountName, err)
	}

	// Check if the given container exists.
	blobService := storageClient.GetBlobService()
	b.containerName = dataAttrs.Get("container_name").(string)
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
	// TODO: Create container if it does not exists.
	return fmt.Errorf("cannot find container: %s", b.containerName)
}
