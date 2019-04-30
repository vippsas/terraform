package remote

import (
	"context"
	"encoding/base32"
	"fmt"
	"strings"

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

var rawStdEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// maskModule masks all sensitive attributes in a module.
func (s *State) maskModule(i int, module map[string]interface{}) {
	if len(s.resourceProviders) == 0 {
		panic("forgot to set resource providers")
	}

	// List all the secrets from the keyvault.
	secretIDs, err := s.KeyVault.ListSecrets(context.Background())
	if err != nil {
		panic(fmt.Errorf("error listing secrets: %s", err))
	}

	// Delete the resource's attributes that does not exists anymore in the key vault.
	resourceAddresses := s.getAllResourceAttrAddresses()
	for secretIDInBase32 := range secretIDs {
		secretID, err := rawStdEncoding.DecodeString(secretIDInBase32)
		if err != nil {
			panic(err)
		}

		// Delete those that does not exist anymore.
		if _, ok := resourceAddresses[string(secretID)]; !ok {
			if err := s.KeyVault.DeleteSecret(context.Background(), secretIDInBase32); err != nil {
				panic(err)
			}
		}
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
			panic(err)
		}
		schemas = append(schemas, schema)
	}
	var resourceSchemas []map[string]*configschema.Block
	for _, schema := range schemas {
		resourceSchemas = append(resourceSchemas, schema.ResourceTypes)
	}

	// Mask the sensitive resource attributes by moving them to the key vault.
	for resourceName, resource := range module["resources"].(map[string]interface{}) {
		r := resource.(map[string]interface{})

		// Filter sensitive attributes into the key vault.
		primary := r["primary"].(map[string]interface{})
		for _, value := range resourceSchemas {
			// Check if schema for the resource exists in the provider.
			resourceSchema := value[r["type"].(string)]
			if resourceSchema == nil {
				continue
			}

			// Make base32-encoded attribute name in the veins of <module paths>.<resource>.<attribute>
			var path []string
			for _, s := range module["path"].([]interface{}) {
				path = append(path, s.(string))
			}

			// Insert the resource's attributes in the key vault.
			attributes := primary["attributes"].(map[string]interface{})
			for key, value := range attributes {
				s.maskAttributes(
					attributes,
					value.(string),
					path,
					resourceName,
					key,
					strings.Split(key, "."),
					0,
					resourceSchema,
				)
			}
		}
	}
}

// maskAttributes masks the attributes of an resource.
func (s *State) maskAttributes(attributes map[string]interface{}, value string, path []string, resourceName string, key string, keySplitted []string, i int, resourceSchema *configschema.Block) {
	// Check if there exist an attribute.
	if i >= len(keySplitted) {
		return
	}

	// Check if attribute from the block exists in the schema.
	if attribute, ok := resourceSchema.Attributes[keySplitted[i]]; ok {
		// Is resource attribute sensitive?
		if attribute.Sensitive { // then mask.
			encodedAttributeName := rawStdEncoding.EncodeToString([]byte(fmt.Sprintf("%s.%s.%s", strings.Join(path, "."), resourceName, key)))
			if len(encodedAttributeName) > 127 {
				// TODO: Encoded attribute name is too long. Ignoring...
				return
			}
			// Insert value to keyvault here.
			if value == "" {
				return
			}
			version, err := s.KeyVault.InsertSecret(context.Background(), encodedAttributeName, value)
			if err != nil {
				panic(fmt.Sprintf("error inserting secret into key vault: %s", err))
			}

			// Replace attribute value with a reference/pointer to the secret value in the state key vault.
			attributes[key] = secretAttribute{
				ID:      encodedAttributeName,
				Version: version,
			}
		}
	} else {
		// Nope, then check if it exists in the nested block types.
		if block, ok := resourceSchema.BlockTypes[keySplitted[i]]; ok {
			s.maskAttributes(attributes, value, path, resourceName, key, keySplitted, i+2, &block.Block)
		}
	}
}

// getAllResourceAttrAddresses returns all addresses to the resource attributes for the key vault.
func (s *State) getAllResourceAttrAddresses() map[string]struct{} {
	resourceAttrAddr := make(map[string]struct{})
	for _, module := range s.state.Modules {
		for resourceName, resource := range module.Resources {
			for attributeName := range resource.Primary.Attributes {
				resourceAttrAddr[fmt.Sprintf("%s.%s.%s", strings.Join(module.Path, "."), resourceName, attributeName)] = struct{}{}
			}
		}
	}
	return resourceAttrAddr
}

// unmaskModule unmasks all sensitive attributes in a module.
func (s *State) unmaskModule(i int, module map[string]interface{}) {
	for _, resource := range module["resources"].(map[string]interface{}) {
		attributes := resource.(map[string]interface{})["primary"].(map[string]interface{})["attributes"].(map[string]interface{})
		for key, value := range attributes {
			if secretAttribute, ok := value.(map[string]interface{}); ok {
				secret, err := s.KeyVault.GetSecret(context.Background(), secretAttribute["id"].(string), secretAttribute["version"].(string))
				if err != nil {
					panic(fmt.Sprintf("error getting secret from key vault: %s", err))
				}
				attributes[key] = secret
			}
		}
	}
}
