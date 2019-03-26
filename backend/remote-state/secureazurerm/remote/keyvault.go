package remote

import "github.com/Azure/azure-sdk-for-go/services/keyvault/v7.0/keyvault"

type KeyVault struct {
	client keyvault.BaseClient
}

// getSecretVersion gets the secret version from ID.
func getSecretVersion(ID string) string {
	i := len(ID) - 1
	for ID[i] != '/' {
		i--
	}
	return ID[i+1 : len(ID)]
}

func New() KeyVault {
	return KeyVault{}
}

// Insert inserts a secret into the key vault. Returns the version.
func (k *KeyVault) Insert(secret string) string {
	return ""
}
