package keyvault

import (
	"context"
	"fmt"

	KV "github.com/Azure/azure-sdk-for-go/services/keyvault/2016-10-01/keyvault"
	"github.com/Azure/azure-sdk-for-go/services/keyvault/mgmt/2016-10-01/keyvault"
	"github.com/hashicorp/terraform/backend/remote-state/secureazurerm/remote/auth"

	"github.com/Azure/go-autorest/autorest"
)

// KeyVault represents an Azure Key Vault.
type KeyVault struct {
	resourceGroupName string
	vaultBaseURI      string
	vaultClient       keyvault.VaultsClient
	keyClient         KV.BaseClient
}

// New creates a new Azure Key Vault.
func New(ctx context.Context, resourceGroupName string, vaultName string, subscriptionID string, mgmtAuthorizer autorest.Authorizer) (KeyVault, error) {
	k := KeyVault{
		resourceGroupName: resourceGroupName,
		vaultClient:       keyvault.NewVaultsClient(subscriptionID),
		keyClient:         KV.New(),
	}
	k.vaultClient.Authorizer = mgmtAuthorizer
	vault, err := k.vaultClient.Get(ctx, resourceGroupName, vaultName)
	if err != nil {
		return k, fmt.Errorf("error getting key vault: %s", err)
	}
	k.vaultBaseURI = *vault.Properties.VaultURI

	k.keyClient.Authorizer, err = auth.NewVault()
	if err != nil {
		return k, err
	}
	return k, nil
}
