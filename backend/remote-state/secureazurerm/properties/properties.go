package properties

import (
	"github.com/Azure/azure-sdk-for-go/arm/resources/resources"
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

	// Authorizers and clients.
	MgmtAuthorizer autorest.Authorizer
	GroupsClient   resources.GroupsClient
}
