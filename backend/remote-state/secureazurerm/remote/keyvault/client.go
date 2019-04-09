package keyvault

import (
	"context"
	"fmt"

	"github.com/Azure/azure-sdk-for-go/arm/resources/resources"
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
	groupsClient      resources.GroupsClient
}

// generateKeyVaultName generates a new random key vault name of max length.
func generateKeyVaultName() (string, error) {
	var a, b string
	a, err := rand.GenLowerAlphas(1)
	if err != nil {
		return "", fmt.Errorf("error generating alphabetic characters: %s", err)
	}
	b, err = rand.GenLowerAlphanums(23)
	if err != nil {
		return "", fmt.Errorf("error generating lowercase alphabetic and numeric characters: %s", err)
	}
	return a + b, nil
}

// Setup creates a new Azure Key Vault.
func Setup(ctx context.Context, props *properties.Properties, workspace string) (*KeyVault, error) {
	k := &KeyVault{
		resourceGroupName: props.ResourceGroupName,
		vaultClient:       keyvault.NewVaultsClient(props.SubscriptionID),
		keyClient:         KV.New(),
		groupsClient:      props.GroupsClient,
		workspace:         workspace,
		location:          props.Location,
	}
	k.vaultClient.Authorizer = props.MgmtAuthorizer

	// TODO: Replace these by saving the key vault name in the state itself.
	group, err := props.GroupsClient.Get(props.ResourceGroupName)
	if err != nil {
		return nil, fmt.Errorf("error getting resource group named %s: %s", props.ResourceGroupName, err)
	}
	if group.Tags == nil {
		k.vaultName, err = generateKeyVaultName()
		if err != nil {
			return nil, fmt.Errorf("error generating key vault name: %s", err)
		}

		tags := make(map[string]*string)
		tags[workspace] = &k.vaultName
		_, err = props.GroupsClient.CreateOrUpdate(
			props.ResourceGroupName,
			resources.Group{
				Location: &props.Location,
				Tags:     &tags,
			},
		)
		if err != nil {
			return k, fmt.Errorf("error updating tags on resource group %s: %s", props.ResourceGroupName, err)
		}
	} else if (*(group.Tags))[workspace] == nil {
		k.vaultName, err = generateKeyVaultName()
		if err != nil {
			return nil, fmt.Errorf("error generating key vault name: %s", err)
		}

		(*group.Tags)[workspace] = &k.vaultName
		_, err = props.GroupsClient.CreateOrUpdate(
			props.ResourceGroupName,
			resources.Group{
				Location: &props.Location,
				Tags:     group.Tags,
			},
		)
		if err != nil {
			return nil, fmt.Errorf("error updating tags on resource group %s: %s", props.ResourceGroupName, err)
		}
	} else {
		k.vaultName = *(*group.Tags)[workspace]
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

// Delete key vault.
func (k *KeyVault) Delete(ctx context.Context) error {
	group, err := k.groupsClient.Get(k.resourceGroupName)
	if err != nil {
		return err
	}
	if _, err := k.vaultClient.Delete(ctx, k.resourceGroupName, k.vaultName); err != nil {
		return fmt.Errorf("error deleting key vault: %s", err)
	}
	for tag := range *group.Tags {
		if tag == k.workspace {
			delete(*group.Tags, tag)
			_, err = k.groupsClient.CreateOrUpdate(
				k.resourceGroupName,
				resources.Group{
					Location: &k.location,
					Tags:     group.Tags,
				},
			)
			if err != nil {
				return fmt.Errorf("error updating tags on resource group %s: %s", k.resourceGroupName, err)
			}
			break
		}
	}
	return nil
}
