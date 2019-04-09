package auth

import (
	"encoding/json"
	"fmt"
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
		// Fetch subscriptionID from environment variable AZURE_SUBSCRIPTION_ID.
		settings, innerErr := auth.GetSettingsFromEnvironment()
		if err != nil {
			err = fmt.Errorf("error creating new authorizer from CLI: %v: error getting settings from environment: %v", err, innerErr)
			return
		}
		props.SubscriptionID = settings.GetSubscriptionID()
		if props.SubscriptionID == "" {
			err = fmt.Errorf("error creating new authorizer from CLI: %v: environment variable %v is not set", err, auth.SubscriptionID)
			return
		}

		// Authorize using MSI.
		props.MgmtAuthorizer, innerErr = settings.GetMSI().Authorizer()
		if innerErr != nil {
			err = fmt.Errorf("error creating new authorizer from CLI: %v: error creating authorizer from environment: %v", err, innerErr)
			return
		}
	} else {
		// Fetch subscriptionID from Azure CLI.
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
		out, err = exec.Command("az", "ad", "signed-in-user", "show", "--output", "json", "--query", "objectId").Output()
		if err = json.Unmarshal(out, &props.ObjectID); err != nil {
			err = fmt.Errorf("error unmarshalling object ID from JSON output from Azure CLI: %s", err)
			return
		}
	}
	err = nil
	return
}

// NewVault creates a new authorizer using keyvault endpoint (don't use the constant, because it is formatted incorrectly).
func NewVault() (authorizer autorest.Authorizer, err error) {
	authorizer, err = auth.NewAuthorizerFromCLIWithResource("https://vault.azure.net")
	if err != nil {
		return
	}
	return
}
