package properties

import (
	"github.com/Azure/go-autorest/autorest"
)

// Properties describes the properties of the state resource group.
type Properties struct {
	// State resource group properties.
	ResourceGroupName,
	Location,
	KeyVaultPrefix,
	SubscriptionID,
	TenantID,
	ObjectID string

	// ObjectIDs that can access the state key vault.
	AccessPolicies []string

	// Authorizers and clients.
	MgmtAuthorizer autorest.Authorizer
}
