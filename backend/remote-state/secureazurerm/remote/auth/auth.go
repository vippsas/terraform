package auth

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"

	"github.com/hashicorp/terraform/backend/remote-state/secureazurerm/properties"

	"github.com/Azure/go-autorest/autorest"
	"github.com/Azure/go-autorest/autorest/azure"
	"github.com/Azure/go-autorest/autorest/azure/auth"
)

// NewMgmt creates a new authorizer using resource management endpoint.
func NewMgmt() (props properties.Properties, err error) {
	// Try authorizing using Azure CLI, which will use the resource: https://management.azure.com/.
	props.MgmtAuthorizer, err = auth.NewAuthorizerFromCLIWithResource(azure.PublicCloud.ResourceManagerEndpoint)
	if err != nil {
		err = fmt.Errorf("error creating new authorizer from CLI with resource %s: %v", azure.PublicCloud.ResourceManagerEndpoint, err)
		return
	}
	// Fetch subscriptionID and tenantID from Azure CLI.
	var out []byte
	out, err = exec.Command("az", "account", "show", "--output", "json").Output()
	if err != nil {
		err = fmt.Errorf("error fetching subscription id using Azure CLI: %s", err)
		return
	}
	var m map[string]interface{}
	if err = json.Unmarshal(out, &m); err != nil {
		err = fmt.Errorf("error unmarshalling subscription ID and tenant ID from JSON output from Azure CLI: %s", err)
		return
	}
	props.SubscriptionID = m["id"].(string)
	props.TenantID = m["tenantId"].(string)
	user := m["user"].(map[string]interface{})

	// Get the objectID of the signed-in user.
	userType := user["type"].(string)
	switch userType {
	case "servicePrincipal":
		clientID := user["name"].(string)
		out, err = exec.Command("az", "ad", "sp", "show", "--id", clientID, "--output", "json", "--query", "objectId").Output()
		if err != nil {
			err = fmt.Errorf("error getting service principal: %s", err)
			return
		}
		os.Setenv("ARM_CLIENT_ID", clientID)
		os.Setenv("ARM_CLIENT_SECRET", os.Getenv("servicePrincipalKey")) // defined in the agent after enabling a setting.
		os.Setenv("ARM_SUBSCRIPTION_ID", props.SubscriptionID)
		os.Setenv("ARM_TENANT_ID", props.TenantID)
	case "user":
		out, err = exec.Command("az", "ad", "signed-in-user", "show", "--output", "json", "--query", "objectId").Output()
		if err != nil {
			err = fmt.Errorf("error getting signed-in user: %s", err)
			return
		}
	default:
		err = fmt.Errorf("unknown user type")
		return
	}
	if err = json.Unmarshal(out, &props.ObjectID); err != nil {
		err = fmt.Errorf("error unmarshalling object ID from JSON output from Azure CLI: %s", err)
		return
	}
	err = nil
	return
}

// NewVault creates a new authorizer using keyvault endpoint (don't use the constant, because it is formatted incorrectly).
func NewVault() (authorizer autorest.Authorizer, err error) {
	vaultEndpoint := "https://vault.azure.net"
	authorizer, err = auth.NewAuthorizerFromCLIWithResource(vaultEndpoint)
	if err != nil {
		err = fmt.Errorf("error creating new authorizer from CLI with resource %s: %v", vaultEndpoint, err)
		return
	}
	return
}
