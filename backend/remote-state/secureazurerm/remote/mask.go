package remote

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"strconv"

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

// mask masks all sensitive attributes in a resource state.
func (s *State) mask(r *common.ResourceState) error {
	var err error

	// Get resource providers.
	if s.Props.ContextOpts.Config == nil {
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
		s.Props.ContextOpts.Config = config
	}
	reqd := terraform.ConfigTreeDependencies(s.Props.ContextOpts.Config, s.state).AllPluginRequirements()
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
			if err = s.maskAttributes(r.Module, r.Type, r.Name, i, attributes, schema); err != nil {
				return fmt.Errorf("error masking attributes: %s", err)
			}
			if instance.AttributesRaw, err = json.Marshal(attributes); err != nil {
				return fmt.Errorf("error marshalling attributes: %s", err)
			}
		}
	}

	return nil
}

// maskAttributes masks the attributes of a resource.
func (s *State) maskAttributes(moduleName, typ, name string, i int, attributes map[string]interface{}, schema *configschema.Block) error {
	var err error
	for attributeName, attributeValue := range attributes {
		// Check if attribute from the block exists in the schema.
		if attribute, ok := schema.Attributes[attributeName]; ok && attribute.Sensitive { // Is resource attribute sensitive? Then mask.
			// Tag secret with related state info.
			tags := make(map[string]*string)
			tags["module"] = &moduleName
			tags["type"] = &typ
			tags["name"] = &name
			a := attributeName
			tags["attribute"] = &a
			countIndex := strconv.Itoa(i)
			tags["count.index"] = &countIndex

			var f func(interface{}, map[string]*string) (interface{}, error)
			f = func(attributeValue interface{}, tags map[string]*string) (interface{}, error) {
				m := make(map[string]interface{})
				switch v := attributeValue.(type) {
				case string, nil:
					// Set existing secret name.
					var secretName string
					for secretID, secretValue := range s.secretIDs {
						if *secretValue.Tags["module"] == *tags["module"] && *secretValue.Tags["type"] == *tags["type"] && *secretValue.Tags["name"] == *tags["name"] && *secretValue.Tags["attribute"] == *tags["attribute"] && *secretValue.Tags["count.index"] == *tags["count.index"] {
							if tagIndex, ok := secretValue.Tags["index"]; ok {
								if index, ok := tags["index"]; ok && *tagIndex != *index {
									break
								}
							}
							if tagKey, ok := secretValue.Tags["key"]; ok {
								if key, ok := tags["key"]; ok && *tagKey != *key {
									break
								}
							}
							secretName = secretID
							break
						}
					}
					// If not existing, generate a new one.
					if secretName == "" {
						retry := 0
						const maxRetries = 3
						for ; retry < maxRetries; retry++ {
							// Generate secret name for the attribute.
							secretName, err = generateLowerAlphanumericChars(32) // it's as long as the version string in length.
							if err != nil {
								return nil, fmt.Errorf("error generating secret name: %s", err)
							}
							// Check for the highly unlikely secret name collision.
							if _, ok := s.secretIDs[secretName]; ok {
								continue // name collision! retrying...
							}
							s.secretIDs[secretName] = keyvault.SecretMetadata{Tags: tags}
							break
						}
						if retry >= maxRetries {
							return nil, fmt.Errorf("error generating random secret name %d times", maxRetries)
						}
					}
					// Set value in keyvault.
					var sv string
					if sv, ok = v.(string); !ok {
						sv = ""
					}
					version, err := s.KeyVault.SetSecret(context.Background(), secretName, sv, tags)
					if err != nil {
						return nil, fmt.Errorf("error inserting secret into key vault: %s", err)
					}
					// Replace attribute value with a reference/pointer to the secret value in the state key vault.
					m["type"] = "string"
					m["id"] = secretName
					m["version"] = version
					return m, nil
				case []interface{}:
					m["type"] = "[]interface{}"
					var l []interface{}
					for i, v := range v {
						mtags := make(map[string]*string)
						for k, v := range tags {
							mtags[k] = v
						}
						index := strconv.Itoa(i)
						mtags["index"] = &index
						k, err := f(v, mtags)
						if err != nil {
							return nil, err
						}
						l = append(l, k)
					}
					m["value"] = l
					return m, nil
				case map[string]interface{}:
					m["type"] = "map[string]interface{}"
					kvmap := make(map[string]interface{})
					for key, val := range v {
						mtags := make(map[string]*string)
						for k, v := range tags {
							mtags[k] = v
						}
						kcopy := key
						mtags["key"] = &kcopy
						k, err := f(val, mtags)
						if err != nil {
							return nil, err
						}
						kvmap[key] = k
					}
					m["value"] = kvmap
				}
				return nil, fmt.Errorf("got attribute value of unknown type: %v", attributeValue)
			}
			if attributes[attributeName], err = f(attributeValue, tags); err != nil {
				return fmt.Errorf("error masking attribute %s with value %v: %s", attributeName, attributeValue, err)
			}
		} else {
			// Nope, then check if it exists in the nested block types.
			if block, ok := schema.BlockTypes[attributeName]; ok {
				if err := s.maskAttributes(moduleName, typ, name, i, attributes, &block.Block); err != nil {
					return fmt.Errorf("error masking attributes in block type: %s", err)
				}
			}
		}
	}

	return nil
}

// unmask unmasks all sensitive attributes in resource states.
func (s *State) unmask(rs *[]common.ResourceState) error {
	for i := range *rs {
		r := &(*rs)[i]
		for j := range r.Instances {
			instance := &r.Instances[j]
			var attributes map[string]interface{}
			var err error
			if err = json.Unmarshal(instance.AttributesRaw, &attributes); err != nil {
				return fmt.Errorf("error unmarshalling attributes: %s", err)
			}
			for key, value := range attributes {
				if secretReference, ok := value.(map[string]interface{}); ok {
					var f func(map[string]interface{}) (interface{}, bool, error)
					f = func(secretAttribute map[string]interface{}) (secretAttributeValue interface{}, cont bool, err error) {
						t, ok := secretAttribute["type"].(string)
						if !ok {
							cont = true
							return
						}
						switch t {
						case "string":
							id, ok := secretAttribute["id"].(string)
							if !ok {
								cont = true
								return
							}
							version, ok := secretAttribute["version"].(string)
							if !ok {
								cont = true
								return
							}
							secretAttributeValue, err = s.KeyVault.GetSecret(context.Background(), id, version)
							if err != nil {
								err = fmt.Errorf("error getting secret from key vault: %s", err)
								return
							}
							return
						case "[]interface{}":
							var l []interface{}
							for _, v := range secretAttribute["value"].([]interface{}) {
								secretAttributeValue, cont, err = f(v.(map[string]interface{}))
								if cont {
									return
								}
								if err != nil {
									return
								}
								l = append(l, secretAttributeValue)
							}
							secretAttributeValue = l
							return
						case "map[string]interface{}":
							kvmap := make(map[string]interface{})
							for k, v := range secretAttribute["value"].(map[string]interface{}) {
								secretAttributeValue, cont, err = f(v.(map[string]interface{}))
								if cont {
									return
								}
								if err != nil {
									return
								}
								kvmap[k] = secretAttributeValue
							}
							secretAttributeValue = kvmap
							return
						}
						err = fmt.Errorf("unknown sensitive attribute type: %s", t)
						return
					}
					var cont bool
					if attributes[key], cont, err = f(secretReference); cont {
						continue
					} else if err != nil {
						return fmt.Errorf("error unmasking attribute %s with reference %v: %s", key, secretReference, err)
					}
				}
			}
			if instance.AttributesRaw, err = json.Marshal(&attributes); err != nil {
				return fmt.Errorf("error marshalling attributes: %s", err)
			}
		}
	}
	return nil
}
