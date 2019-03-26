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
func NewMgmt() (authorizer autorest.Authorizer, subscriptionID string, err error) {
	// Try authorizing using Azure CLI, which will use the resource: https://management.azure.com/.
	authorizer, err = auth.NewAuthorizerFromCLIWithResource(azure.PublicCloud.ResourceManagerEndpoint)
	if err != nil {
		// Fetch subscriptionID from environment variable AZURE_SUBSCRIPTION_ID.
		settings, err := auth.GetSettingsFromEnvironment()
		if err != nil {
			return authorizer, "", fmt.Errorf("error getting settings from environment: %s", err)
		}
		subscriptionID = settings.GetSubscriptionID()
		if subscriptionID == "" {
			return authorizer, "", fmt.Errorf("environment variable %s is not set", auth.SubscriptionID)
		}
		// Authorize using MSI.
		var innerErr error
		authorizer, innerErr = settings.GetMSI().Authorizer()
		if innerErr != nil {
			return authorizer, "", fmt.Errorf("error creating authorizer from CLI: %s: error creating authorizer from environment: %s", err, innerErr)
		}
	} else {
		// Fetch subscriptionID from Azure CLI.
		out, err := exec.Command("az", "account", "show", "--output", "json", "--query", "id").Output()
		if err != nil {
			return authorizer, "", fmt.Errorf("error fetching subscription id using Azure CLI: %s", err)
		}
		if err = json.Unmarshal(out, &subscriptionID); err != nil {
			return authorizer, "", fmt.Errorf("error unmarshalling JSON output from Azure CLI: %s", err)
		}
	}
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
