package secureazurerm

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/Azure/azure-sdk-for-go/arm/resources/resources"
	"github.com/Azure/go-autorest/autorest/to"
	"github.com/hashicorp/terraform/backend"
	"github.com/hashicorp/terraform/backend/remote-state/secureazurerm/properties"
	"github.com/hashicorp/terraform/backend/remote-state/secureazurerm/remote/account"
	"github.com/hashicorp/terraform/backend/remote-state/secureazurerm/remote/auth"
	"github.com/hashicorp/terraform/helper/schema"
	"github.com/mitchellh/cli"
	"github.com/mitchellh/colorstring"
)

// Backend maintains the remote state in Azure.
type Backend struct {
	schema.Backend

	mu sync.Mutex

	// CLI
	CLI      cli.Ui
	CLIColor *colorstring.Colorize

	container *account.Container

	props properties.Properties
}

// New creates a new backend for remote state stored in Azure storage account and key vault.
func New() backend.Backend {
	b := &Backend{
		Backend: schema.Backend{
			// Fields in backend {}. Ensure that all values are stored only in the configuration files.
			Schema: map[string]*schema.Schema{
				"name": {
					Type:        schema.TypeString,
					Required:    true,
					Description: "The name of the state's storage account and naming prefix for the state's key vaults.",
				},
				"location": {
					Type:        schema.TypeString,
					Required:    true,
					Description: "The geographical location where the state is stored.",
				},
				"access_policies": {
					Type:     schema.TypeList,
					Optional: true,
					Elem: &schema.Schema{
						Type: schema.TypeString,
					},
				},
			},
		},
	}
	b.Backend.ConfigureFunc = b.configure
	return b
}

// configure bootstraps the Azure resources needed to use this backend.
func (b *Backend) configure(ctx context.Context) error {
	var err error
	b.props, err = auth.NewMgmt()
	if err != nil {
		return fmt.Errorf("error creating new mgmt authorizer: %s", err)
	}

	// Get the data attributes from the "backend"-block.
	attributes := schema.FromContextBackendConfig(ctx)
	b.props.Name = attributes.Get("name").(string)
	b.props.Location = attributes.Get("location").(string)
	for _, resourceAddress := range attributes.Get("access_policies").([]interface{}) {
		sa := []string{"root"}
		splitted := strings.Split(resourceAddress.(string), ".")
		for i := 0; i < len(splitted); {
			if splitted[i] == "module" {
				sa = append(sa, splitted[i+1])
				i += 2
			} else {
				sa = append(sa, splitted[i])
				i++
			}
		}
		b.props.AccessPolicies = append(b.props.AccessPolicies, strings.Join(sa, "."))
	}

	// Setup the resource group for terraform.State.
	groupsClient := resources.NewGroupsClient(b.props.SubscriptionID)
	groupsClient.Authorizer = b.props.MgmtAuthorizer
	// Check if the resource group already exists.
	_, err = groupsClient.Get(b.props.Name)
	if err != nil { // resource group does not exist.
		// Create the resource group.
		_, err = groupsClient.CreateOrUpdate(
			b.props.Name,
			resources.Group{
				Location: to.StringPtr(b.props.Location),
			},
		)
		if err != nil {
			return fmt.Errorf("error creating a resource group %s: %s", b.props.Name, err)
		}
	}

	// Setup a container in the Azure storage account.
	if b.container, err = account.Setup(ctx, &b.props, "states"); err != nil {
		return fmt.Errorf("error creating container: %s", err)
	}

	return nil
}
