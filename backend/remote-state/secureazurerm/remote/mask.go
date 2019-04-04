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

// SecretAttr is a sensitive attribute that is located as a secret in the Azure key vault.
type secretAttr struct {
	ID      string `json:"id"`      // ID of the secret.
	Version string `json:"version"` // Version of the secret.
}

/*
// interpAttr is a sensitive attribute interpolated from somewhere.
type interpAttr struct {
	Type      string // Type of resource.
	ID        string // ID of the resource.
	Attribute string // Attribute name of resource.
}
*/

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

// pathEqual compares if the path of two modules are equal.
func pathEqual(a []interface{}, b []string) bool {
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
	for resourceName, resource := range module["resources"].(map[string]interface{}) {
		r := resource.(map[string]interface{})
		primary := r["primary"].(map[string]interface{})
		s.maskResource(i, resourceName, primary["attributes"].(map[string]interface{}))
	}
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
	pretty.Printf("secretIDs:\n%# v\n", secretIDs)

	for key := range secretIDs {
		bs, err := rawStdEncoding.DecodeString(key)
		if err != nil {
			panic(err)
		}
		pretty.Printf("bs:\n%v\n", string(bs))

		// Delete those that does not exist anymore.
		keyVaultIDs := strings.Split(string(bs), ".")
		if keyVaultIDs[len(keyVaultIDs)-1] != name {
			continue
		}
		if _, ok := attrs[keyVaultIDs[len(keyVaultIDs)-2]]; !ok {
			fmt.Printf("Deleting secret: %s\n", key)
			if err := s.KeyVault.DeleteSecret(context.Background(), key); err != nil {
				panic(err)
			}
		}
	}

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
