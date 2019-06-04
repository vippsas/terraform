package keyvault

import (
	"context"
	"fmt"

	KV "github.com/Azure/azure-sdk-for-go/services/keyvault/2016-10-01/keyvault"
)

// getSecretVersion gets the secret version from ID.
func getSecretVersion(ID string) string {
	i := len(ID) - 1
	for ID[i] != '/' {
		i--
	}
	return ID[i+1 : len(ID)]
}

// InsertSecret inserts a secret into the key vault. Returns the version.
func (k *KeyVault) InsertSecret(ctx context.Context, name string, value string) (string, error) {
	contentType := "text/plain;charset=UTF-8"
	bundle, err := k.keyClient.SetSecret(ctx, k.vaultURI, name, KV.SecretSetParameters{Value: &value, ContentType: &contentType})
	if err != nil {
		return "", fmt.Errorf("error inserting secret: %s", err)
	}
	return getSecretVersion(*bundle.ID), nil
}

// DeleteSecret deletes the secret named after the given name-parameter.
func (k *KeyVault) DeleteSecret(ctx context.Context, name string) error {
	_, err := k.keyClient.DeleteSecret(ctx, k.vaultURI, name)
	return err
}

// GetSecret gets the secret named name from the key vault.
func (k *KeyVault) GetSecret(ctx context.Context, name string, version string) (string, error) {
	bundle, err := k.keyClient.GetSecret(ctx, k.vaultURI, name, version)
	if err != nil {
		return "", fmt.Errorf("error getting secret: %s", err)
	}
	return *bundle.Value, nil
}

// ListSecrets returns the names of the secrets.
func (k *KeyVault) ListSecrets(ctx context.Context) (map[string]struct{}, error) {
	secrets, err := k.keyClient.GetSecrets(ctx, k.vaultURI, nil)
	if err != nil {
		return nil, fmt.Errorf("error getting secrets from key vault: %s", err)
	}

	m := make(map[string]struct{})
	for err := secrets.NextWithContext(ctx); err == nil; {
		for _, value := range secrets.Values() {
			m[*value.ID] = struct{}{}
		}
	}
	return m, nil
}
