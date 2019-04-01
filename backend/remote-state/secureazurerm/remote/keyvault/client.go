package keyvault

import (
	"context"
	"fmt"

	"github.com/Azure/azure-sdk-for-go/arm/resources/resources"
	KV "github.com/Azure/azure-sdk-for-go/services/keyvault/2016-10-01/keyvault"
	"github.com/Azure/azure-sdk-for-go/services/keyvault/mgmt/2016-10-01/keyvault"
	"github.com/hashicorp/terraform/backend/remote-state/secureazurerm/rand"
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
	groupsClient      resources.GroupsClient
	workspace         string
	location          string
}

func genKVName() string {
	var a, b string
	a, err := rand.GenLowerAlphas(1)
	if err != nil {
		panic(err)
	}
	b, err = rand.GenLowerAlphanums(23)
	if err != nil {
		panic(err)
	}
	return a + b
}

// Setup creates a new Azure Key Vault.
func Setup(ctx context.Context, resourceGroupName, location, workspace, subscriptionID, tenantID, objectID string, mgmtAuthorizer autorest.Authorizer, groupsClient resources.GroupsClient) (KeyVault, error) {
	k := KeyVault{
		resourceGroupName: resourceGroupName,
		vaultClient:       keyvault.NewVaultsClient(subscriptionID),
		keyClient:         KV.New(),
		groupsClient:      groupsClient,
		workspace:         workspace,
		location:          location,
	}
	k.vaultClient.Authorizer = mgmtAuthorizer

	group, err := groupsClient.Get(resourceGroupName)
	if err != nil {
		return k, err
	}
	if group.Tags == nil {
		k.vaultName = genKVName()

		tags := make(map[string]*string)
		tags[workspace] = &k.vaultName
		_, err = groupsClient.CreateOrUpdate(
			resourceGroupName,
			resources.Group{
				Location: &location,
				Tags:     &tags,
			},
		)
		if err != nil {
			return k, fmt.Errorf("error updating tags on resource group %s: %s", resourceGroupName, err)
		}
	} else if (*(group.Tags))[workspace] == nil {
		k.vaultName = genKVName()

		(*group.Tags)[workspace] = &k.vaultName
		_, err = groupsClient.CreateOrUpdate(
			resourceGroupName,
			resources.Group{
				Location: &location,
				Tags:     group.Tags,
			},
		)
		if err != nil {
			return k, fmt.Errorf("error updating tags on resource group %s: %s", resourceGroupName, err)
		}
	} else {
		k.vaultName = *(*group.Tags)[workspace]
	}

	vault, err := k.vaultClient.Get(ctx, resourceGroupName, k.vaultName)
	if err != nil {
		tenantID, err := uuid.FromString(tenantID)
		if err != nil {
			return k, fmt.Errorf("error converting tenant ID-string to UUID: %s", err)
		}
		vault, err = k.vaultClient.CreateOrUpdate(ctx, resourceGroupName, k.vaultName, keyvault.VaultCreateOrUpdateParameters{
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
