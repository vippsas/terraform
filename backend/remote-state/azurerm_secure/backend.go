package azurerm_secure

import (
	"context"
	"fmt"

	armStorage "github.com/Azure/azure-sdk-for-go/arm/storage"
	"github.com/Azure/azure-sdk-for-go/storage"
	"github.com/Azure/go-autorest/autorest"
	"github.com/Azure/go-autorest/autorest/adal"
	"github.com/Azure/go-autorest/autorest/azure"
	"github.com/hashicorp/terraform/backend"
	"github.com/hashicorp/terraform/helper/schema"
)

type Backend struct {
	*schema.Backend

	// The fields below are set from configure.
	blobClient storage.BlobStorageClient
	containerName      string
	keyName            string
	leaseID            string
}

type BackendConfig struct {
	// Resource Group:
	ResourceGroupName  string

	// Azure Storage Account:
	StorageAccountName string
	AccessKey          string

	// Azure Key Vault:
	KeyVaultName       string

	// Credentials:
	Environment        string
	ClientID           string
	ClientSecret       string
	SubscriptionID     string
	TenantID           string
}

// New creates a new backend for remote state stored in Azure storage account and key vault.
func New() backend.Backend {
	s := &schema.Backend{
		Schema: map[string]*schema.Schema{
			// Resource group:
			"resource_group_name": {
				Type:        schema.TypeString,
				Optional:    true,
				Description: "The resource group name.",
			},

			// Azure Storage Account:
			"storage_account_name": {
				Type:        schema.TypeString,
				Required:    true,
				Description: "The name of the storage account.",
			},

			"access_key": { // storage account access key.
				Type:        schema.TypeString,
				Optional:    true,
				Description: "The access key.",
				DefaultFunc: schema.EnvDefaultFunc("ACCESS_KEY", ""),
			},

			"container_name": {
				Type:        schema.TypeString,
				Required:    true,
				Description: "The container name.",
			},

			"blob_name": { // renamed from "key", so developers won't confuse it with keys in key vault.
				Type:        schema.TypeString,
				Required:    true,
				Description: "The blob name.",
			},

			// Azure Key Vault:
			"key_vault_name": {
				Type:        schema.TypeString,
				Required:    true,
				Description: "The key vault name.",
			},

			// Credentials:
			"environment": { // optional, automatically set to "public" if empty.
				Type:        schema.TypeString,
				Optional:    true,
				Description: "The Azure cloud environment.",
				DefaultFunc: schema.EnvDefaultFunc("ENVIRONMENT", ""),
			},

			"tenant_id": {
				Type:        schema.TypeString,
				Optional:    true,
				Description: "The tenant ID.",
				DefaultFunc: schema.EnvDefaultFunc("TENANT_ID", ""),
			},

			"subscription_id": {
				Type:        schema.TypeString,
				Optional:    true,
				Description: "The subscription ID.",
				DefaultFunc: schema.EnvDefaultFunc("SUBSCRIPTION_ID", ""),
			},

			"client_id": {
				Type:        schema.TypeString,
				Optional:    true,
				Description: "The client ID.",
				DefaultFunc: schema.EnvDefaultFunc("CLIENT_ID", ""),
			},

			"client_secret": {
				Type:        schema.TypeString,
				Optional:    true,
				Description: "The client secret.",
				DefaultFunc: schema.EnvDefaultFunc("CLIENT_SECRET", ""),
			},
		},
	}

	result := &Backend{Backend: s}
	result.Backend.ConfigureFunc = result.configure
	return result
}

func (b *Backend) configure(ctx context.Context) error {
	if b.containerName != "" {
		return nil
	}

	// Get the resource data from the backend configuration.
	data := schema.FromContextBackendConfig(ctx)
	b.containerName = data.Get("container_name").(string)
	b.keyName = data.Get("blob_name").(string)
	config := BackendConfig{
		// Resource Group:
		ResourceGroupName:  data.Get("resource_group_name").(string),

		// Azure Storage Account:
		StorageAccountName: data.Get("storage_account_name").(string),
		AccessKey:          data.Get("access_key").(string),

		// Azure Key Vault:
		KeyVaultName:       data.Get("key_vault_name").(string),

		// Credentials:
		Environment:        data.Get("environment").(string),
		TenantID:           data.Get("tenant_id").(string),
		SubscriptionID:     data.Get("subscription_id").(string),
		ClientID:           data.Get("client_id").(string),
		ClientSecret:       data.Get("client_secret").(string),
	}

	blobClient, err := getBlobClient(config)
	if err != nil {
		return err
	}
	b.blobClient = blobClient

	return nil
}

func getBlobClient(config BackendConfig) (storage.BlobStorageClient, error) {
	var client storage.BlobStorageClient

	env, err := getAzureEnvironment(config.Environment)
	if err != nil {
		return client, err
	}

	accessKey, err := getAccessKey(config, env)
	if err != nil {
		return client, err
	}

	storageClient, err := storage.NewClient(config.StorageAccountName, accessKey, env.StorageEndpointSuffix, storage.DefaultAPIVersion, true)
	if err != nil {
		return client, fmt.Errorf("error creating storage client for storage account %q: %s", config.StorageAccountName, err)
	}

	client = storageClient.GetBlobService()
	return client, nil
}

func getKeyVaultClient() {
	return nil
}

func getAccessKey(config BackendConfig, env azure.Environment) (string, error) {
	if config.AccessKey != "" {
		return config.AccessKey, nil
	}

	rgOk := config.ResourceGroupName != ""
	subOk := config.SubscriptionID != ""
	clientIDOk := config.ClientID != ""
	clientSecretOK := config.ClientSecret != ""
	tenantIDOk := config.TenantID != ""
	if !rgOk || !subOk || !clientIDOk || !clientSecretOK || !tenantIDOk {
		return "", fmt.Errorf("resource_group_name and credentials must be provided when access_key is absent")
	}

	oauthConfig, err := adal.NewOAuthConfig(env.ActiveDirectoryEndpoint, config.TenantID)
	if err != nil {
		return "", err
	}

	spt, err := adal.NewServicePrincipalToken(*oauthConfig, config.ClientID, config.ClientSecret, env.ResourceManagerEndpoint)
	if err != nil {
		return "", err
	}

	accountsClient := armStorage.NewAccountsClientWithBaseURI(env.ResourceManagerEndpoint, config.SubscriptionID)
	accountsClient.Authorizer = autorest.NewBearerAuthorizer(spt)

	keys, err := accountsClient.ListKeys(config.ResourceGroupName, config.StorageAccountName)
	if err != nil {
		return "", fmt.Errorf("error retrieving keys for storage account %q: %s", config.StorageAccountName, err)
	}

	if keys.Keys == nil {
		return "", fmt.Errorf("nil key returned for storage account %q", config.StorageAccountName)
	}

	accessKeys := *keys.Keys
	return *accessKeys[0].Value, nil
}

func getAzureEnvironment(environment string) (azure.Environment, error) {
	if environment == "" {
		return azure.PublicCloud, nil
	}

	env, err := azure.EnvironmentFromName(environment)
	if err != nil {
		// try again with wrapped value to support readable values like german instead of AZUREGERMANCLOUD
		var innerErr error
		env, innerErr = azure.EnvironmentFromName(fmt.Sprintf("AZURE%sCLOUD", environment))
		if innerErr != nil {
			return env, fmt.Errorf("invalid 'environment' configuration: %s", err)
		}
	}

	return env, nil
}
