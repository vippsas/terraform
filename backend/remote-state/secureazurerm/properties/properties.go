package properties

import (
	"github.com/Azure/go-autorest/autorest"
	"github.com/hashicorp/terraform/terraform"
	uuid "github.com/satori/go.uuid"
)

// Properties describes the properties of the state resource group.
type Properties struct {
	// State resource group properties.
	Name,
	Location,
	KeyVaultPrefix,
	SubscriptionID,
	ObjectID string

	TenantID uuid.UUID

	// ObjectIDs that can access the state key vault.
	AccessPolicies []string

	StorageAccountResourceID string // The fully-qualified resource ID of the state's storage account.

	// Authorizers and clients.
	MgmtAuthorizer autorest.Authorizer

	ContextOpts *terraform.ContextOpts
}
