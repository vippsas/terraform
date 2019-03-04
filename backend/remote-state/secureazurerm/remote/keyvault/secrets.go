package keyvault

import (
	"context"
	"fmt"

	KV "github.com/Azure/azure-sdk-for-go/services/keyvault/2016-10-01/keyvault"
)

// getID gets the secret name (ID without the base URI) from the key vault's ID.
func getID(ID string) string {
	i := len(ID) - 1
	for ID[i] != '/' {
		i--
	}
	return ID[i+1 : len(ID)]
}

// SetSecret sets a secret in a key vault. If the secret does not exist, it creates it. Returns the version of the secret.
func (k *KeyVault) SetSecret(ctx context.Context, name, value string, tags map[string]*string) (string, error) {
	// Get latest secret.
	var maxResults int32 = 1
	result, err := k.keyClient.GetSecretVersions(ctx, k.vaultURI, name, &maxResults)
	if err != nil {
		return "", fmt.Errorf("error getting secret versions: %s", err)
	}
	values := result.Values()
	if len(values) > 0 {
		secretVersion := getID(*values[0].ID)
		secretValue, err := k.GetSecret(ctx, name, secretVersion)
		if err != nil {
			return "", fmt.Errorf("error getting secret: %s", err)
		}
		// If it's still the same, don't insert a new secret.
		if secretValue == value {
			return secretVersion, nil
		}
	}

	// Set/insert a new secret.
	contentType := "text/plain;charset=UTF-8"
	bundle, err := k.keyClient.SetSecret(ctx, k.vaultURI, name, KV.SecretSetParameters{Value: &value, ContentType: &contentType, Tags: tags})
	if err != nil {
		return "", fmt.Errorf("error inserting secret: %s", err)
	}

	// Return the current secret version.
	return getID(*bundle.ID), nil
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

// SecretMetadata contains the metadata of the secret.
type SecretMetadata struct {
	Tags map[string]*string
}

// ListSecrets returns the names of the secrets.
func (k *KeyVault) ListSecrets(ctx context.Context) (map[string]SecretMetadata, error) {
	secrets, err := k.keyClient.GetSecrets(ctx, k.vaultURI, nil)
	if err != nil {
		return nil, fmt.Errorf("error getting secrets from key vault: %s", err)
	}
	secretMap := make(map[string]SecretMetadata)
	for {
		values := secrets.Values()
		if values == nil {
			break
		}
		for _, value := range values {
			secretMap[getID(*value.ID)] = SecretMetadata{
				Tags: value.Tags,
			}
		}
		if err := secrets.NextWithContext(ctx); err != nil {
			break
		}
	}
	return secretMap, nil
}
