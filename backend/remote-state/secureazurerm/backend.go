package secureazurerm

import (
	"context"
	"fmt"
	"sync"

	"github.com/hashicorp/terraform/backend"
	"github.com/hashicorp/terraform/backend/remote-state/secureazurerm/remote/account"
	"github.com/hashicorp/terraform/backend/remote-state/secureazurerm/remote/auth"
	"github.com/hashicorp/terraform/backend/remote-state/secureazurerm/remote/keyvault"
	"github.com/hashicorp/terraform/helper/schema"
	"github.com/hashicorp/terraform/terraform"
	"github.com/mitchellh/cli"
	"github.com/mitchellh/colorstring"
)

// Backend maintains the remote state in Azure.
// TODO: Store the backend-configuration in a (separate) container instead of .terraform-dir?
type Backend struct {
	schema.Backend
	mu sync.Mutex

	// CLI
	CLI         cli.Ui
	CLIColor    *colorstring.Colorize
	ContextOpts *terraform.ContextOpts
	// never ask for input. always validate. always run in automation.

	container account.Container
	keyVault  keyvault.KeyVault
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
	backendAttributes := schema.FromContextBackendConfig(ctx)

	// Resource Group:
	resourceGroupName := backendAttributes.Get("resource_group_name").(string)
	fmt.Printf("TODO: Provision resource group: %s\n", resourceGroupName)
	// 1. Check if the given resource group exists.
	//   - If not, create it!
	// (idempotent)

	// Azure Key Vault:
	keyVaultName := backendAttributes.Get("key_vault_name").(string)
	fmt.Printf("TODO: Provision key vault: %s\n", keyVaultName)
	// 2. Check if the key vault has been made in the resource group.
	//   - If not, create it!
	// (idempotent)

	// Azure Storage Account:
	storageAccountName := backendAttributes.Get("storage_account_name").(string)
	// 2. Check if the storage account has been made in the resource group.
	//   - If not, create it!
	// (idempotent)
	containerName := backendAttributes.Get("container_name").(string)

	mgmtAuthorizer, subscriptionID, err := auth.NewMgmt()
	if err != nil {
		return fmt.Errorf("error creating new mgmt authorizer: %s", err)
	}

	// Setup the Azure key vault.
	b.keyVault, err = keyvault.New(resourceGroupName, keyVaultName, subscriptionID, mgmtAuthorizer)
	if err != nil {
		return fmt.Errorf("error creating key vault: %s", err)
	}

	/*
		//var version string
		secret, err := b.keyVault.GetSecret(ctx, "test1")
		if err != nil {
			return err
		}
	*/

	version, err := b.keyVault.InsertSecret(ctx, "bao", "ååøøø")
	if err != nil {
		return err
	}
	fmt.Printf("version: %s", version)

	// Setup a container in the Azure storage account.
	if b.container, err = account.New(ctx, mgmtAuthorizer, subscriptionID, resourceGroupName, storageAccountName, containerName); err != nil {
		return fmt.Errorf("error creating container: %s", err)
	}
	return nil
}
