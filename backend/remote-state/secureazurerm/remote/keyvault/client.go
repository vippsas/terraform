package keyvault

import (
	KV "github.com/Azure/azure-sdk-for-go/services/keyvault/2016-10-01/keyvault"
	"github.com/Azure/azure-sdk-for-go/services/keyvault/mgmt/2016-10-01/keyvault"
	"github.com/hashicorp/terraform/backend/remote-state/secureazurerm/remote/auth"

	"github.com/Azure/go-autorest/autorest"
)

// KeyVault represents an Azure Key Vault.
type KeyVault struct {
	resourceGroupName string
	vaultName         string
	vaultClient       keyvault.VaultsClient
	keyClient         KV.BaseClient
}

// New creates a new Azure Key Vault.
func New(resourceGroupName string, vaultName string, subscriptionID string, mgmtAuthorizer autorest.Authorizer) (KeyVault, error) {
	kv := KeyVault{
		resourceGroupName: resourceGroupName,
		vaultName:         vaultName,
		vaultClient:       keyvault.NewVaultsClient(subscriptionID),
		keyClient:         KV.New(),
	}
	kv.vaultClient.Authorizer = mgmtAuthorizer
	var err error
	kv.keyClient.Authorizer, err = auth.NewVault()
	if err != nil {
		return kv, err
	}
	return kv, nil
}
