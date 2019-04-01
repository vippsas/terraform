package keyvault

import (
	"context"
	"fmt"

	KV "github.com/Azure/azure-sdk-for-go/services/keyvault/2016-10-01/keyvault"
	"github.com/Azure/azure-sdk-for-go/services/keyvault/mgmt/2016-10-01/keyvault"
	"github.com/hashicorp/terraform/backend/remote-state/secureazurerm/remote/auth"
	uuid "github.com/satori/go.uuid"

	"github.com/Azure/go-autorest/autorest"
	"github.com/Azure/go-autorest/autorest/to"
)

// KeyVault represents an Azure Key Vault.
type KeyVault struct {
	resourceGroupName string
	vaultName         string
	vaultURI          string
	vaultClient       keyvault.VaultsClient
	keyClient         KV.BaseClient
}

// Setup creates a new Azure Key Vault.
func Setup(ctx context.Context, resourceGroupName, location, vaultName, subscriptionID, tenantID, objectID string, mgmtAuthorizer autorest.Authorizer) (KeyVault, error) {
	k := KeyVault{
		resourceGroupName: resourceGroupName,
		vaultName:         vaultName,
		vaultClient:       keyvault.NewVaultsClient(subscriptionID),
		keyClient:         KV.New(),
	}
	k.vaultClient.Authorizer = mgmtAuthorizer

	vault, err := k.vaultClient.Get(ctx, resourceGroupName, vaultName)
	if err != nil {
		tenantID, err := uuid.FromString(tenantID)
		if err != nil {
			return k, fmt.Errorf("error converting tenant ID-string to UUID: %s", err)
		}
		vault, err = k.vaultClient.CreateOrUpdate(ctx, resourceGroupName, vaultName, keyvault.VaultCreateOrUpdateParameters{
			Location: to.StringPtr(location),
			Properties: &keyvault.VaultProperties{
				TenantID: &tenantID,
				Sku: &keyvault.Sku{
					Family: to.StringPtr("A"),
					Name:   keyvault.Standard,
				},
				AccessPolicies: &[]keyvault.AccessPolicyEntry{
					keyvault.AccessPolicyEntry{
						TenantID: &tenantID,
						ObjectID: &objectID,
						Permissions: &keyvault.Permissions{
							Secrets: &[]keyvault.SecretPermissions{
								keyvault.SecretPermissionsGet,
								keyvault.SecretPermissionsSet,
							},
						},
					},
				},
			},
		})
		if err != nil {
			return k, fmt.Errorf("error creating key vault: %s", err)
		}
	}
	k.vaultURI = *vault.Properties.VaultURI

	k.keyClient.Authorizer, err = auth.NewVault()
	if err != nil {
		return k, err
	}
	return k, nil
}

// Delete key vault.
func (k *KeyVault) Delete(ctx context.Context) error {
	if _, err := k.vaultClient.Delete(ctx, k.resourceGroupName, k.vaultName); err != nil {
		return fmt.Errorf("error deleting key vault: %s", err)
	}
	return nil
}
