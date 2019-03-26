package keyvault

import (
	"github.com/Azure/azure-sdk-for-go/services/keyvault/v7.0/keyvault"
	"github.com/Azure/go-autorest/autorest"
)

// KeyVault represents an Azure Key Vault.
type KeyVault struct {
	client keyvault.BaseClient
}

// New creates a new Azure Key Vault.
func New(authorizer autorest.Authorizer) (KeyVault, error) {
	kv := KeyVault{}
	kv.client.Authorizer = authorizer
	return kv, nil
}
