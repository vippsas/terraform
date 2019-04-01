package secureazurerm

import (
	"context"
	"fmt"
	"sync"

	"github.com/Azure/azure-sdk-for-go/arm/resources/resources"
	"github.com/Azure/go-autorest/autorest"
	"github.com/Azure/go-autorest/autorest/to"
	"github.com/hashicorp/terraform/backend"
	"github.com/hashicorp/terraform/backend/remote-state/secureazurerm/remote/account"
	"github.com/hashicorp/terraform/backend/remote-state/secureazurerm/remote/auth"
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

	resourceGroupName,
	keyVaultPrefix,
	subscriptionID,
	tenantID,
	objectID string

	mgmtAuthorizer autorest.Authorizer
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
				"key_vault_prefix": {
					Type:        schema.TypeString,
					Required:    true,
					Description: "The key vault prefix.",
				},

				// Storage Account:
				"storage_account_name": {
					Type:        schema.TypeString,
					Required:    true,
					Description: "The storage account name.",
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
	attrs := schema.FromContextBackendConfig(ctx)

	// Resource Group:
	b.resourceGroupName = attrs.Get("resource_group_name").(string)
	fmt.Printf("TODO: Provision resource group: %s\n", b.resourceGroupName)
	// 1. Check if the given resource group exists.
	//   - If not, create it!
	// (idempotent)
	// Tags: <workspace>: <kvname>
	// Azure Key Vault:
	b.keyVaultPrefix = attrs.Get("key_vault_prefix").(string)
	// TODO: 1 random lowercase character (cannot start with a number) and 23 random lowercase alphanumeric characters.

	// 2. Check if the key vault has been made in the resource group.
	//   - If not, create it!
	// (idempotent)

	// Azure Storage Account:
	storageAccountName := attrs.Get("storage_account_name").(string)
	// 2. Check if the storage account has been made in the resource group.
	//   - If not, create it!
	// (idempotent)

	var err error
	b.mgmtAuthorizer, b.subscriptionID, b.tenantID, b.objectID, err = auth.NewMgmt()
	if err != nil {
		return fmt.Errorf("error creating new mgmt authorizer: %s", err)
	}

	// Setup the resource group for terraform.State.
	groupsClient := resources.NewGroupsClient(b.subscriptionID)
	groupsClient.Authorizer = b.mgmtAuthorizer
	// Check if the resource group already exists.
	_, err = groupsClient.Get(b.resourceGroupName)
	if err != nil { // does not exist.
		// Create the resource group.
		_, err = groupsClient.CreateOrUpdate(
			b.resourceGroupName,
			resources.Group{
				Location: to.StringPtr("westeurope"),
			},
		)
		if err != nil {
			return fmt.Errorf("error creating a resource group %s: %s", b.resourceGroupName, err)
		}
	}

	// Setup a container in the Azure storage account.
	if b.container, err = account.Setup(ctx, b.mgmtAuthorizer, b.subscriptionID, b.resourceGroupName, storageAccountName, "tfstate"); err != nil {
		return fmt.Errorf("error creating container: %s", err)
	}

	return nil
}
