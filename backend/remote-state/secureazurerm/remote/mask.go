package remote

import (
	"context"
	"encoding/base32"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform/terraform"
	"github.com/kr/pretty"
)

// Module is used to report which attributes are sensitive or not.
type Module struct {
	Path      []string
	Resources map[string]map[string]bool
}

// secretAttr is a sensitive attribute that is located as a secret in the Azure key vault.
type secretAttr struct {
	ID      string `json:"id"`      // ID of the secret.
	Version string `json:"version"` // Version of the secret.
}

// copyResources copies the resources from a module i.
func (s *State) copyResources(i int, resources map[string]*terraform.InstanceDiff) {
	for name, resource := range resources {
		s.modules[i].Resources[name] = make(map[string]bool)
		for key, value := range resource.Attributes {
			s.modules[i].Resources[name][key] = value.Sensitive
		}
	}
}

// copyModules copies the modules (only the relevant data).
func (s *State) copyModules(modules []*terraform.ModuleDiff) {
	if len(s.modules) != len(modules) {
		s.modules = make([]Module, len(modules))
	}
	for i, module := range modules {
		s.modules[i].Path = make([]string, len(module.Path))
		copy(s.modules[i].Path, module.Path)
		s.modules[i].Resources = make(map[string]map[string]bool)
		s.copyResources(i, module.Resources)
	}
	// DEBUG: Print which attributes are sensitive. ~ bao.
	/*
		for i, module := range s.modules {
			fmt.Printf("Module: %d\n", i)
			for name, resource := range module.Resources {
				fmt.Printf("Resource: %s:\n", name)
				for attribute, value := range resource {
					fmt.Printf("  %s: %t\n", attribute, value)
				}
			}
		}
	*/
}

// Report is used to report sensitive attributes to the state.
func (s *State) Report(modules []*terraform.ModuleDiff) {
	// Lock!
	s.mu.Lock()
	defer s.mu.Unlock()

	s.copyModules(modules)
}

// SetResourceProviders sets resource providers.
func (s *State) SetResourceProviders(p []terraform.ResourceProvider) {
	s.resourceProviders = p
}

// isModulePathEqual compares if the path of two modules are equal.
func isModulePathEqual(a []interface{}, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i, val := range a {
		if val.(string) != b[i] {
			return false
		}
	}
	return true
}

// maskModule masks all sensitive attributes in a module.
func (s *State) maskModule(i int, module map[string]interface{}) {
	resources := module["resources"].(map[string]interface{})

	resourceList := []string{}
	for name := range resources {
		resourceList = append(resourceList, strings.Split(name, ".")[0])
	}
	pretty.Printf("resourceList: %# v\n", resourceList)

	/*
		for _, rp := range s.resourceProviders {
				schema, err := rp.GetSchema(&terraform.ProviderSchemaRequest{
					ResourceTypes: resourceList,
				})
				if err != nil {
					panic(err)
				}
				pretty.Printf("schema: %# v\n", schema)
		}
	*/

	for resourceName, resource := range module["resources"].(map[string]interface{}) {
		r := resource.(map[string]interface{})
		primary := r["primary"].(map[string]interface{})
		s.maskResource(i, resourceName, primary["attributes"].(map[string]interface{}))
	}
}

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

var rawStdEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// maskResource masks all sensitive attributes in a resource.
func (s *State) maskResource(i int, name string, attrs map[string]interface{}) {
	pretty.Printf("attrs:\n%# v\n", attrs)

	// List all the secrets from the keyvault.
	secretIDs, err := s.KeyVault.ListSecrets(context.Background())
	if err != nil {
		panic(fmt.Errorf("error listing secrets: %s", err))
	}

	// Delete the resource's attributes that does not exists anymore in the key vault.
	resourceAddresses := s.getAllResourceAttrAddresses()
	for id := range secretIDs {
		bs, err := rawStdEncoding.DecodeString(id)
		if err != nil {
			panic(err)
		}
		pretty.Printf("bs: %# v\n", string(bs))

		// Delete those that does not exist anymore.
		if _, ok := resourceAddresses[string(bs)]; !ok {
			pretty.Printf("Deleting secret: %s\n", id)
			if err := s.KeyVault.DeleteSecret(context.Background(), id); err != nil {
				panic(err)
			}
		}
	}

	// Insert the resource's attributes in the key vault.
	for key, value := range attrs {
		encodedAttrName := rawStdEncoding.EncodeToString([]byte(fmt.Sprintf("%s.%s.%s", strings.Join(s.modules[i].Path, "."), name, key)))

		// Is resource attribute sensitive?
		if s.modules[i].Resources[name][key] { // then mask.
			// Insert value to keyvault here.
			version, err := s.KeyVault.InsertSecret(context.Background(), encodedAttrName, value.(string))
			if err != nil {
				panic(fmt.Sprintf("error inserting secret to key vault: %s", err))
			}
			attrs[key] = secretAttr{
				ID:      encodedAttrName,
				Version: version,
			}
		}
	}
}

// unmaskModule unmasks all sensitive attributes in a module.
func (s *State) unmaskModule(i int, module map[string]interface{}) {
	for resourceName, resource := range module["resources"].(map[string]interface{}) {
		r := resource.(map[string]interface{})
		primary := r["primary"].(map[string]interface{})
		s.unmaskResource(i, resourceName, primary["attributes"].(map[string]interface{}))
	}
}

func (s *State) unmaskResource(i int, name string, attrs map[string]interface{}) {
	for key, value := range attrs {
		if sa, ok := value.(map[string]interface{}); ok {
			secret, err := s.KeyVault.GetSecret(context.Background(), sa["id"].(string), sa["version"].(string))
			if err != nil {
				panic(fmt.Sprintf("error getting secret from key vault: %s", err))
			}
			attrs[key] = secret
		}
	}
}
