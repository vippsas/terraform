package remote

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform/backend/remote-state/secureazurerm/rand"
	"github.com/hashicorp/terraform/config/configschema"
	"github.com/hashicorp/terraform/terraform"
)

// secretAttribute is a sensitive attribute that is located as a secret in the Azure key vault.
type secretAttribute struct {
	ID      string `json:"id"`      // ID of the secret.
	Version string `json:"version"` // Version of the secret.
}

// SetResourceProviders sets resource providers.
func (s *State) SetResourceProviders(p []terraform.ResourceProvider) {
	s.resourceProviders = p
}

// maskModule masks all sensitive attributes in a module.
func (s *State) maskModule(module map[string]interface{}) error {
	if len(s.resourceProviders) == 0 {
		panic("forgot to set resource providers")
	}

	// Get the schemas for the resource attributes.
	resourceList := []string{}
	for name := range module["resources"].(map[string]interface{}) {
		resourceList = append(resourceList, strings.Split(name, ".")[0])
	}
	var schemas []*terraform.ProviderSchema
	for _, rp := range s.resourceProviders {
		schema, err := rp.GetSchema(&terraform.ProviderSchemaRequest{
			ResourceTypes: resourceList,
		})
		if err != nil {
			return fmt.Errorf("error getting resource schemas: %s", err)
		}
		schemas = append(schemas, schema)
	}
	var resourceSchemas []map[string]*configschema.Block
	for _, schema := range schemas {
		resourceSchemas = append(resourceSchemas, schema.ResourceTypes)
	}

	// Mask the sensitive resource attributes by moving them to the key vault.
	for _, resource := range module["resources"].(map[string]interface{}) {
		r := resource.(map[string]interface{})

		// Filter sensitive attributes into the key vault.
		primary := r["primary"].(map[string]interface{})
		for _, value := range resourceSchemas {
			// Check if schema for the resource exists in the provider.
			resourceSchema := value[r["type"].(string)]
			if resourceSchema == nil {
				continue
			}

			// Insert the resource's attributes in the key vault.
			attributes := primary["attributes"].(map[string]interface{})
			for attributeName, attributeValue := range attributes {
				s.maskAttribute(
					attributes,
					attributeValue.(string),
					attributeName,
					strings.Split(attributeName, "."),
					0,
					resourceSchema,
				)
			}
		}
	}

	return nil
}

// maskAttribute masks the attributes of a resource.
func (s *State) maskAttribute(attributes map[string]interface{}, attributeValue string, attributeName string, attributeNameSplitted []string, namePos int, resourceSchema *configschema.Block) error {
	// Check if there exist an attribute.
	if namePos >= len(attributeNameSplitted) {
		return nil
	}

	// Check if attribute from the block exists in the schema.
	if attribute, ok := resourceSchema.Attributes[attributeNameSplitted[namePos]]; ok {
		// Is resource attribute sensitive?
		if attribute.Sensitive { // then mask.
			var secretName string
			var err error
			retry := 0
			maxRetries := 3
			for ; retry < maxRetries; retry++ {
				// Generate secret name for the attribute.
				secretName, err = rand.GenerateLowerAlphanumericChars(32) // it's as long as the version string in length.
				if err != nil {
					return fmt.Errorf("error generating secret name: %s", err)
				}
				// Check for the highly unlikely secret name collision.
				if _, ok := s.secretIDs[secretName]; ok {
					// Name collision! Retrying...
					continue
				}
				break
			}
			if retry >= maxRetries {
				return fmt.Errorf("error generating random secret name %d times", maxRetries)
			}

			// Insert value to keyvault here.
			version, err := s.KeyVault.InsertSecret(context.Background(), secretName, attributeValue)
			if err != nil {
				return fmt.Errorf("error inserting secret into key vault: %s", err)
			}

			// Replace attribute value with a reference/pointer to the secret value in the state key vault.
			attributes[attributeName] = secretAttribute{
				ID:      secretName,
				Version: version,
			}
		}
	} else {
		// Nope, then check if it exists in the nested block types.
		if block, ok := resourceSchema.BlockTypes[attributeNameSplitted[namePos]]; ok {
			s.maskAttribute(attributes, attributeValue, attributeName, attributeNameSplitted, namePos+2, &block.Block)
		}
	}

	return nil
}

// unmaskModule unmasks all sensitive attributes in a module.
func (s *State) unmaskModule(module map[string]interface{}) error {
	for _, resource := range module["resources"].(map[string]interface{}) {
		attributes := resource.(map[string]interface{})["primary"].(map[string]interface{})["attributes"].(map[string]interface{})
		for key, value := range attributes {
			if secretAttribute, ok := value.(map[string]interface{}); ok {
				secretAttributeValue, err := s.KeyVault.GetSecret(context.Background(), secretAttribute["id"].(string), secretAttribute["version"].(string))
				if err != nil {
					return fmt.Errorf("error getting secret from key vault: %s", err)
				}
				attributes[key] = secretAttributeValue
			}
		}
	}
	return nil
}
