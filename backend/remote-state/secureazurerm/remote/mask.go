package remote

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"

	"github.com/hashicorp/terraform/backend/remote-state/secureazurerm/common"
	"github.com/hashicorp/terraform/backend/remote-state/secureazurerm/remote/keyvault"
	"github.com/hashicorp/terraform/configs/configload"
	"github.com/hashicorp/terraform/configs/configschema"
	"github.com/hashicorp/terraform/providers"
	"github.com/hashicorp/terraform/terraform"
)

// generateLowerAlphanumericChars generates a random lowercase alphanumeric string of len n.
func generateLowerAlphanumericChars(n int) (string, error) {
	var chars = []rune("abcdefghijklmnopqrstuvwxyz0123456789")
	b := make([]byte, n)
	_, err := rand.Read(b)
	if err != nil {
		return "", fmt.Errorf("error reading from secure random generator: %s", err)
	}

	var s []rune
	for _, number := range b {
		s = append(s, chars[int(number)%len(chars)])
	}
	return string(s), nil
}

// secretAttribute is a sensitive attribute that is located as a secret in the Azure key vault.
type secretAttribute struct {
	ID      string `json:"id"`      // ID of the secret.
	Version string `json:"version"` // Version of the secret.
}

// mask masks all sensitive attributes in a resource state.
func (s *State) mask(r *common.ResourceState) error {
	// Get resource providers.
	wd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("error getting current working directory: %s", err)
	}
	loader, err := configload.NewLoader(&configload.Config{
		ModulesDir: ".terraform/modules",
	})
	if err != nil {
		return fmt.Errorf("error creating new loader: %s", err)
	}
	config, diags := loader.LoadConfig(wd)
	if diags.HasErrors() {
		return fmt.Errorf("error loading config: %s", diags)
	}
	reqd := terraform.ConfigTreeDependencies(config, s.state).AllPluginRequirements()
	if s.Props.ContextOpts.ProviderSHA256s != nil && !s.Props.ContextOpts.SkipProviderVerify {
		reqd.LockExecutables(s.Props.ContextOpts.ProviderSHA256s)
	}
	providerFactories, errs := s.Props.ContextOpts.ProviderResolver.ResolveProviders(reqd)
	if errs != nil {
		return fmt.Errorf("error resolving providers: %s", errs)
	}
	var provds []providers.Interface
	for _, f := range providerFactories {
		provider, err := f()
		if err != nil {
			return fmt.Errorf("error retrieving provider: %s", err)
		}
		provds = append(provds, provider)
	}

	// Get the schemas for the resource attributes.
	var schemas []providers.Schema
	for _, rp := range provds {
		if schema, ok := rp.GetSchema().ResourceTypes[r.Type]; ok {
			schemas = append(schemas, schema)
		}
	}
	var resourceSchemas []*configschema.Block
	for _, schema := range schemas {
		resourceSchemas = append(resourceSchemas, schema.Block)
	}

	// Mask the sensitive resource attributes by moving them to the key vault.
	for _, schema := range resourceSchemas {
		for i := range r.Instances {
			instance := &r.Instances[i]
			// Insert the resource's attributes in the key vault.
			var attributes map[string]interface{}
			if err = json.Unmarshal(instance.AttributesRaw, &attributes); err != nil {
				return fmt.Errorf("error unmarshalling attributes: %s", err)
			}
			for attributeName, attributeValue := range attributes {
				s.maskAttribute(
					r.Module,
					r.Name,
					attributes,
					attributeName,
					attributeValue,
					schema,
				)
			}
			if instance.AttributesRaw, err = json.Marshal(attributes); err != nil {
				return fmt.Errorf("error marshalling attributes: %s", err)
			}
		}
	}

	return nil
}

// maskAttribute masks the attributes of a resource.
func (s *State) maskAttribute(moduleName string, resourceName string, attributes map[string]interface{}, attributeName string, attributeValue interface{}, schema *configschema.Block) error {
	// Check if attribute from the block exists in the schema.
	if attribute, ok := schema.Attributes[attributeName]; ok {
		// Is resource attribute sensitive?
		if attribute.Sensitive { // then mask.
			// Set existing secret name or generate a new one.
			var secretName string
			var err error
			for secretID, value := range s.secretIDs {
				if *value.Tags["module"] == moduleName && *value.Tags["resource"] == resourceName && *value.Tags["attribute"] == attributeName {
					secretName = secretID
					break
				}
			}

			// Tag secret with related state info.
			tags := make(map[string]*string)
			tags["module"] = &moduleName
			tags["resource"] = &resourceName
			tags["attribute"] = &attributeName

			if secretName == "" {
				retry := 0
				maxRetries := 3
				for ; retry < maxRetries; retry++ {
					// Generate secret name for the attribute.
					secretName, err = generateLowerAlphanumericChars(32) // it's as long as the version string in length.
					if err != nil {
						return fmt.Errorf("error generating secret name: %s", err)
					}
					// Check for the highly unlikely secret name collision.
					if _, ok := s.secretIDs[secretName]; ok {
						// Name collision! Retrying...
						continue
					}
					s.secretIDs[secretName] = keyvault.SecretMetadata{Tags: tags}
					break
				}
				if retry >= maxRetries {
					return fmt.Errorf("error generating random secret name %d times", maxRetries)
				}
			}

			// Set value in keyvault.
			version, err := s.KeyVault.SetSecret(context.Background(), secretName, attributeValue.(string), tags)
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
		if block, ok := schema.BlockTypes[attributeName]; ok {
			s.maskAttribute(
				moduleName,
				resourceName,
				attributes,
				attributeName,
				attributeValue,
				&block.Block,
			)
		}
	}

	return nil
}

// unmask unmasks all sensitive attributes in a resource state.
func (s *State) unmask(rs []common.ResourceState) error {
	for _, resource := range rs {
		for _, instance := range resource.Instances {
			var attributes map[string]interface{}
			if err := json.Unmarshal(instance.AttributesRaw, &attributes); err != nil {
				return fmt.Errorf("error unmarshalling attributes: %s", err)
			}
			for key, value := range attributes {
				if secretAttribute, ok := value.(secretAttribute); ok {
					secretAttributeValue, err := s.KeyVault.GetSecret(context.Background(), secretAttribute.ID, secretAttribute.Version)
					if err != nil {
						return fmt.Errorf("error getting secret from key vault: %s", err)
					}
					attributes[key] = secretAttributeValue
				}
			}
		}
	}
	return nil
}
