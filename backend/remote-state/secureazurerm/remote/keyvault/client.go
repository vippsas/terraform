package keyvault

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/hashicorp/terraform/backend/remote-state/secureazurerm/remote/account/blob"
	"github.com/hashicorp/terraform/state"

	KV "github.com/Azure/azure-sdk-for-go/services/keyvault/2016-10-01/keyvault"
	"github.com/Azure/azure-sdk-for-go/services/keyvault/mgmt/2016-10-01/keyvault"
	"github.com/hashicorp/terraform/backend/remote-state/secureazurerm/properties"
	"github.com/hashicorp/terraform/backend/remote-state/secureazurerm/rand"
	"github.com/hashicorp/terraform/backend/remote-state/secureazurerm/remote/auth"
	uuid "github.com/satori/go.uuid"

	"github.com/Azure/go-autorest/autorest/to"
)

// KeyVault represents an Azure Key Vault.
type KeyVault struct {
	vaultName   string
	vaultURI    string
	vaultClient keyvault.VaultsClient
	keyClient   KV.BaseClient

	resourceGroupName string
	workspace         string
	location          string
}

// Name returns the name of the key vault.
func (k *KeyVault) Name() string {
	return k.vaultName
}

// generateKeyVaultName generates a new random key vault name of max key vault name length.
func generateKeyVaultName() (string, error) {
	var singleAlphaChar, alphanumerics string
	singleAlphaChar, err := rand.GenerateLowerAlphabeticChars(1)
	if err != nil {
		return "", fmt.Errorf("error generating alphabetic characters: %s", err)
	}
	alphanumerics, err = rand.GenerateLowerAlphanumericChars(23)
	if err != nil {
		return "", fmt.Errorf("error generating lowercase alphabetic and numeric characters: %s", err)
	}
	return singleAlphaChar + alphanumerics, nil
}

// Setup creates a new Azure Key Vault.
func Setup(ctx context.Context, blob *blob.Blob, props *properties.Properties, workspace string) (*KeyVault, error) {
	k := &KeyVault{
		resourceGroupName: props.ResourceGroupName,
		vaultClient:       keyvault.NewVaultsClient(props.SubscriptionID),
		keyClient:         KV.New(),
		workspace:         workspace,
		location:          props.Location,
	}
	k.vaultClient.Authorizer = props.MgmtAuthorizer

	payload, err := blob.Get()
	if err != nil {
		return nil, fmt.Errorf("error getting blob: %s", err)
	}
	var stateMap map[string]interface{}
	err = json.Unmarshal(payload.Data, &stateMap)
	if err != nil {
		panic(err)
	}

	if stateMap["keyVaultName"] == nil {
		// Set a new generated key vault name.
		k.vaultName, err = generateKeyVaultName()
		if err != nil {
			return nil, fmt.Errorf("error generating key vault name: %s", err)
		}
		stateMap["keyVaultName"] = k.vaultName

		// Lock/Lease blob.
		lockInfo := state.NewLockInfo()
		lockInfo.Operation = "SetupKeyVault"
		leaseID, err := blob.Lock(lockInfo)
		if err != nil {
			return nil, fmt.Errorf("error locking blob: %s", err)
		}
		defer blob.Unlock(leaseID)

		// Marshal state map to JSON.
		data, err := json.MarshalIndent(stateMap, "", "    ")
		if err != nil {
			return nil, fmt.Errorf("error marshalling state map to JSON: %s", err)
		}
		data = append(data, '\n')

		// Put the JSON into the blob.
		err = blob.Put(data)
		if err != nil {
			return nil, fmt.Errorf("error putting state to blob: %s", err)
		}
	} else {
		k.vaultName = stateMap["keyVaultName"].(string)
	}

	// Setup the key vault.
	vault, err := k.vaultClient.Get(ctx, props.ResourceGroupName, k.vaultName)
	if err != nil {
		tenantID, err := uuid.FromString(props.TenantID)
		if err != nil {
			return nil, fmt.Errorf("error converting tenant ID-string to UUID: %s", err)
		}
		vault, err = k.vaultClient.CreateOrUpdate(ctx, props.ResourceGroupName, k.vaultName, keyvault.VaultCreateOrUpdateParameters{
			Location: to.StringPtr(props.Location),
			Properties: &keyvault.VaultProperties{
				TenantID: &tenantID,
				Sku: &keyvault.Sku{
					Family: to.StringPtr("A"),
					Name:   keyvault.Standard,
				},
				AccessPolicies: &[]keyvault.AccessPolicyEntry{
					keyvault.AccessPolicyEntry{
						TenantID: &tenantID,
						ObjectID: &props.ObjectID,
						Permissions: &keyvault.Permissions{
							Secrets: &[]keyvault.SecretPermissions{
								keyvault.SecretPermissionsList,
								keyvault.SecretPermissionsGet,
								keyvault.SecretPermissionsSet,
								keyvault.SecretPermissionsDelete,
							},
						},
					},
				},
			},
		})
		if err != nil {
			return nil, fmt.Errorf("error creating key vault: %s", err)
		}
	}
	k.vaultURI = *vault.Properties.VaultURI

	k.keyClient.Authorizer, err = auth.NewVault()
	if err != nil {
		return nil, fmt.Errorf("error creating new vault authorizer: %s", err)
	}
	return k, nil
}

// ManagedIdentity contains the ID of a managed service principal.
type ManagedIdentity struct {
	PrincipalID string
	TenantID    string
}

// AddIDToAccessPolicies adds a managed identity to the key vault's access policies.
func (k *KeyVault) AddIDToAccessPolicies(ctx context.Context, identity *ManagedIdentity) error {
	tenantID, err := uuid.FromString(identity.TenantID)
	if err != nil {
		return fmt.Errorf("error converting tenant ID-string to UUID: %s", err)
	}
	accessPoliciesToAdd := []keyvault.AccessPolicyEntry{
		keyvault.AccessPolicyEntry{
			TenantID: &tenantID,
			ObjectID: &identity.PrincipalID,
			Permissions: &keyvault.Permissions{
				Secrets: &[]keyvault.SecretPermissions{
					keyvault.SecretPermissionsGet,
				},
			},
		},
	}
	_, err = k.vaultClient.UpdateAccessPolicy(ctx, k.resourceGroupName, k.vaultName, keyvault.Add, keyvault.VaultAccessPolicyParameters{
		Properties: &keyvault.VaultAccessPolicyProperties{
			AccessPolicies: &accessPoliciesToAdd,
		},
	})
	if err != nil {
		return fmt.Errorf("error updating key vault: %s", err)
	}
	return nil
}

// RemoveIDFromAccessPolicies removes the service principal ID provided from the key vault's access policies.
func (k *KeyVault) RemoveIDFromAccessPolicies(ctx context.Context, tenantID uuid.UUID, objectID string) error {
	_, err := k.vaultClient.UpdateAccessPolicy(ctx, k.resourceGroupName, k.vaultName, keyvault.Remove, keyvault.VaultAccessPolicyParameters{
		Properties: &keyvault.VaultAccessPolicyProperties{
			AccessPolicies: &[]keyvault.AccessPolicyEntry{
				keyvault.AccessPolicyEntry{
					TenantID: &tenantID,
					ObjectID: &objectID,
				},
			},
		},
	})
	if err != nil {
		return fmt.Errorf("error deleting from the key vault's access policy: %s", err)
	}
	return nil
}

// GetAccessPolicies returns the access policies of the key vault.
func (k *KeyVault) GetAccessPolicies(ctx context.Context) ([]keyvault.AccessPolicyEntry, error) {
	vault, err := k.vaultClient.Get(ctx, k.resourceGroupName, k.vaultName)
	if err != nil {
		return nil, fmt.Errorf("error getting access policies: %s", err)
	}
	return *vault.Properties.AccessPolicies, nil
}

// Delete key vault.
func (k *KeyVault) Delete(ctx context.Context) error {
	if _, err := k.vaultClient.Delete(ctx, k.resourceGroupName, k.vaultName); err != nil {
		return fmt.Errorf("error deleting key vault: %s", err)
	}
	return nil
}
