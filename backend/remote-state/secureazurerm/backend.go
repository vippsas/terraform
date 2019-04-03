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

	container *account.Container

	resourceGroupName,
	location,
	keyVaultPrefix,
	subscriptionID,
	tenantID,
	objectID string

	mgmtAuthorizer autorest.Authorizer
	groupsClient   resources.GroupsClient
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
				"location": {
					Type:        schema.TypeString,
					Required:    true,
					Description: "The location where the state is stored.",
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
	// Tags: <workspace>: <kvname>

	b.location = attrs.Get("location").(string)

	var err error
	b.mgmtAuthorizer, b.subscriptionID, b.tenantID, b.objectID, err = auth.NewMgmt()
	if err != nil {
		return fmt.Errorf("error creating new mgmt authorizer: %s", err)
	}

	// Setup the resource group for terraform.State.
	b.groupsClient = resources.NewGroupsClient(b.subscriptionID)
	b.groupsClient.Authorizer = b.mgmtAuthorizer
	// Check if the resource group already exists.
	_, err = b.groupsClient.Get(b.resourceGroupName)
	if err != nil { // does not exist.
		// Create the resource group.
		_, err = b.groupsClient.CreateOrUpdate(
			b.resourceGroupName,
			resources.Group{
				Location: to.StringPtr(b.location),
			},
		)
		if err != nil {
			return fmt.Errorf("error creating a resource group %s: %s", b.resourceGroupName, err)
		}
	}

	// Setup a container in the Azure storage account.
	if b.container, err = account.Setup(ctx, b.mgmtAuthorizer, b.subscriptionID, b.resourceGroupName, b.location, "states"); err != nil {
		return fmt.Errorf("error creating container: %s", err)
	}

	return nil
}
