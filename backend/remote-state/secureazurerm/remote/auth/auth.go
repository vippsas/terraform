package auth

import (
	"encoding/json"
	"fmt"
	"os/exec"

	"github.com/Azure/go-autorest/autorest"
	"github.com/Azure/go-autorest/autorest/azure"
	"github.com/Azure/go-autorest/autorest/azure/auth"
)

// NewMgmt creates a new authorizer using resource mgmt endpoint.
func NewMgmt() (authorizer autorest.Authorizer, subscriptionID, tenantID, objectID string, err error) {
	// Try authorizing using Azure CLI, which will use the resource: https://management.azure.com/.
	authorizer, err = auth.NewAuthorizerFromCLIWithResource(azure.PublicCloud.ResourceManagerEndpoint)
	if err != nil {
		// Fetch subscriptionID from environment variable AZURE_SUBSCRIPTION_ID.
		var settings auth.EnvironmentSettings
		settings, err = auth.GetSettingsFromEnvironment()
		if err != nil {
			err = fmt.Errorf("error getting settings from environment: %s", err)
			return
		}
		subscriptionID = settings.GetSubscriptionID()
		if subscriptionID == "" {
			err = fmt.Errorf("environment variable %s is not set", auth.SubscriptionID)
			return
		}
		// Authorize using MSI.
		var innerErr error
		authorizer, innerErr = settings.GetMSI().Authorizer()
		if innerErr != nil {
			err = fmt.Errorf("error creating authorizer from CLI: %s: error creating authorizer from environment: %s", err, innerErr)
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
		subscriptionID = m["id"].(string)
		tenantID = m["tenantId"].(string)
		out, err = exec.Command("az", "ad", "signed-in-user", "show", "--output", "json", "--query", "objectId").Output()
		if err = json.Unmarshal(out, &objectID); err != nil {
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
