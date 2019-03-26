package remote

import (
	"fmt"

	"github.com/hashicorp/terraform/terraform"
)

// Module is used to report which attributes are sensitive or not.
type Module struct {
	Path      []string
	Resources map[string]map[string]bool
}

// secretAttr is a sensitive attribute that is located as a secret in the Azure key vault.
type secretAttr struct {
	ID      string `json: "id"`      // ID of the secret.
	Version string `json: "version"` // Version of the secret.
}

// interpAttr is a sensitive attribute interpolated from somewhere.
type interpAttr struct {
	Type      string // Type of resource.
	ID        string // ID of the resource.
	Attribute string // Attribute name of resource.
}

/*
// mask masks a sensitive attribute.
func mask(attr string) interface{} {
	if attr != "" {
		return interpAttr{Attribute: attr}
	}
	return interpAttr{Attribute: ""}
}

// unmask unmasks a masked sensitive attribute.
func unmask(attr interface{}) (string, error) {
	if s, ok := attr.(string); ok {
		return s, nil
	}
	if attr, ok := attr.(interpAttr); ok {
		return "", nil
	}
	if attr, ok := attr.(secretAttr); ok {
		return "", nil
	}
	return "", fmt.Errorf("error unmasking attributes")
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
		fmt.Printf("%s:\n", resourceName)
		r := resource.(map[string]interface{})
		primary := r["primary"].(map[string]interface{})
		s.maskResource(i, resourceName, primary["attributes"].(map[string]interface{}))
	}
}

// maskResource masks all sensitive attributes in a resource.
func (s *State) maskResource(i int, name string, attrs map[string]interface{}) {
	for key, value := range attrs {
		if s.modules[i].Resources[name][key] {
			// TODO: Insert value to keyvault here.
			attrs[key] = secretAttr{
				Name:    "NameTest",
				Version: "VerTest",
			}
			fmt.Printf("  %s: %v\n", key, value)
		}
	}
}
