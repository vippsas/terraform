package secureazurerm

import (
	"context"
	"fmt"
	"sync"

	"github.com/Azure/azure-sdk-for-go/arm/resources/resources"
	"github.com/Azure/go-autorest/autorest/to"
	"github.com/hashicorp/terraform/backend"
	"github.com/hashicorp/terraform/backend/remote-state/secureazurerm/properties"
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

	props *properties.Properties
}

// New creates a new backend for remote state stored in Azure storage account and key vault.
func New() backend.Backend {
	b := &Backend{
		Backend: schema.Backend{
			// Fields in backend {}. Ensure that all values are stored only in the configuration files.
			Schema: map[string]*schema.Schema{
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
	b.props.ResourceGroupName = attrs.Get("resource_group_name").(string)
	// Tags: <workspace>: <kvname>

	b.props.Location = attrs.Get("location").(string)

	var err error
	b.props, err = auth.NewMgmt()
	if err != nil {
		return fmt.Errorf("error creating new mgmt authorizer: %s", err)
	}

	// Setup the resource group for terraform.State.
	b.props.GroupsClient = resources.NewGroupsClient(b.props.SubscriptionID)
	b.props.GroupsClient.Authorizer = b.props.MgmtAuthorizer
	// Check if the resource group already exists.
	_, err = b.props.GroupsClient.Get(b.props.ResourceGroupName)
	if err != nil { // does not exist.
		// Create the resource group.
		_, err = b.props.GroupsClient.CreateOrUpdate(
			b.props.ResourceGroupName,
			resources.Group{
				Location: to.StringPtr(b.props.Location),
			},
		)
		if err != nil {
			return fmt.Errorf("error creating a resource group %s: %s", b.props.ResourceGroupName, err)
		}
	}

	// Setup a container in the Azure storage account.
	if b.container, err = account.Setup(ctx, b.props, "states"); err != nil {
		return fmt.Errorf("error creating container: %s", err)
	}

	return nil
}
