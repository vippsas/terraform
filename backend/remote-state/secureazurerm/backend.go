package secureazurerm

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/Azure/azure-sdk-for-go/arm/resources/resources"
	"github.com/Azure/go-autorest/autorest/azure"
	azauth "github.com/Azure/go-autorest/autorest/azure/auth"
	"github.com/Azure/go-autorest/autorest/to"
	"github.com/hashicorp/terraform/backend"
	"github.com/hashicorp/terraform/backend/remote-state/secureazurerm/properties"
	"github.com/hashicorp/terraform/backend/remote-state/secureazurerm/remote/account"
	"github.com/hashicorp/terraform/helper/schema"
	"github.com/mitchellh/cli"
	"github.com/mitchellh/colorstring"
	uuid "github.com/satori/go.uuid"
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
					DefaultFunc: schema.EnvDefaultFunc("SECURE_ARM_NAME", ""),
				},
				"location": {
					Type:        schema.TypeString,
					Required:    true,
					Description: "The geographical location where the state is stored.",
					DefaultFunc: schema.EnvDefaultFunc("SECURE_ARM_LOCATION", ""),
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

	// Try authorizing using Azure CLI, which will use the resource: https://management.azure.com/.
	b.props.MgmtAuthorizer, err = azauth.NewAuthorizerFromCLIWithResource(azure.PublicCloud.ResourceManagerEndpoint)
	if err != nil {
		return fmt.Errorf("error creating new authorizer from CLI with resource %s: %v", azure.PublicCloud.ResourceManagerEndpoint, err)
	}
	// Fetch subscriptionID and tenantID from Azure CLI.
	out, err := exec.Command("az", "account", "show", "--output", "json").Output()
	if err != nil {
		return fmt.Errorf("error fetching subscription id using Azure CLI: %s", err)
	}
	var loggedInAccount map[string]interface{}
	if err = json.Unmarshal(out, &loggedInAccount); err != nil {
		return fmt.Errorf("error unmarshalling subscription ID and tenant ID from JSON output from Azure CLI: %s", err)
	}
	b.props.SubscriptionID = loggedInAccount["id"].(string)
	tenantID := loggedInAccount["tenantId"].(string)
	if b.props.TenantID, err = uuid.FromString(tenantID); err != nil {
		return fmt.Errorf("error converting tenant ID-string to UUID: %s", err)
	}
	user := loggedInAccount["user"].(map[string]interface{})

	// Get the objectID of the signed-in user.
	userType := user["type"].(string)
	switch userType {
	case "servicePrincipal":
		clientID := user["name"].(string)
		out, err = exec.Command("az", "ad", "sp", "show", "--id", clientID, "--output", "json", "--query", "objectId").Output()
		if err != nil {
			return fmt.Errorf("error getting service principal: %s", err)
		}
		os.Setenv("ARM_CLIENT_ID", clientID)
		os.Setenv("ARM_CLIENT_SECRET", os.Getenv("servicePrincipalKey")) // defined in the agent after enabling a setting.
		os.Setenv("ARM_SUBSCRIPTION_ID", b.props.SubscriptionID)
		os.Setenv("ARM_TENANT_ID", tenantID)
	case "user":
		out, err = exec.Command("az", "ad", "signed-in-user", "show", "--output", "json", "--query", "objectId").Output()
		if err != nil {
			return fmt.Errorf("error getting signed-in user: %s", err)
		}
	default:
		return fmt.Errorf("unknown user type")
	}
	if err = json.Unmarshal(out, &b.props.ObjectID); err != nil {
		return fmt.Errorf("error unmarshalling object ID from JSON output from Azure CLI: %s", err)
	}

	// Get the data attributes from the "backend"-block.
	attributes := schema.FromContextBackendConfig(ctx)
	b.props.Name = attributes.Get("name").(string)
	if b.props.Name == "" {
		return fmt.Errorf("name is empty")
	}
	b.props.Location = attributes.Get("location").(string)
	if b.props.Location == "" {
		return fmt.Errorf("location is empty")
	}
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
