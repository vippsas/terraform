package remote

import (
	"context"
	"encoding/base32"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform/config/configschema"
	"github.com/hashicorp/terraform/terraform"
	"github.com/kr/pretty"
)

// secretAttr is a sensitive attribute that is located as a secret in the Azure key vault.
type secretAttr struct {
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
	// Setup.
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
		pretty.Printf("secretID: %# v\n", string(secretID))

		// Delete those that does not exist anymore.
		if _, ok := resourceAddresses[string(secretID)]; !ok {
			pretty.Printf("Deleting secret: %s\n", secretIDInBase32)
			if err := s.KeyVault.DeleteSecret(context.Background(), secretIDInBase32); err != nil {
				panic(err)
			}
		}
	}

	resources := module["resources"].(map[string]interface{})

	// Get the schemas for the resource attributes.
	resourceList := []string{}
	for name := range resources {
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
			resourceSchema := value[r["type"].(string)]
			if resourceSchema == nil {
				continue
			}
			pretty.Printf("resourceSchema: %# v\n", resourceSchema)

			attrs := primary["attributes"].(map[string]interface{})
			pretty.Printf("attrs: %# v\n", attrs)

			// Insert the resource's attributes in the key vault.
			for key, value := range attrs {
				var path []string
				for _, s := range module["path"].([]interface{}) {
					path = append(path, s.(string))
				}
				encodedAttrName := rawStdEncoding.EncodeToString([]byte(fmt.Sprintf("%s.%s.%s", strings.Join(path, "."), resourceName, key)))

				// Check if attribute exist in the schema.
				if block, ok := resourceSchema.Attributes[strings.Split(key, ".")[0]]; ok {
					// Is resource attribute sensitive?
					if block.Sensitive { // then mask.
						// Insert value to keyvault here.
						version, err := s.KeyVault.InsertSecret(context.Background(), encodedAttrName, value.(string))
						if err != nil {
							panic(fmt.Sprintf("error inserting secret to key vault: %s", err))
						}
						attrs[key] = secretAttr{
							ID:      encodedAttrName,
							Version: version,
						}
					} else {
						pretty.Printf("not sensitive: %# v\n", key)
					}
				} else {
					pretty.Printf("not ok: %# v\n", key)
				}
			}
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
		r := resource.(map[string]interface{})
		primary := r["primary"].(map[string]interface{})
		attributes := primary["attributes"].(map[string]interface{})
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
