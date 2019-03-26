package remote

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sync"

	"github.com/hashicorp/terraform/backend/remote-state/secureazurerm/remote/account/blob"
	"github.com/hashicorp/terraform/state"
	"github.com/hashicorp/terraform/terraform"
)

// State contains the remote state.
type State struct {
	mu sync.Mutex

	Blob *blob.Blob // client to communicate with the blob storage.

	state, // in-memory state.
	readState *terraform.State // state read from the blob.

	modules []Module // contains what attributes are sensitive.
}

// Module is used to report which attributes are sensitive or not.
type Module struct {
	Path      []string
	Resources map[string]map[string]bool
}

// secretAttribute is a sensitive attribute that is located as a secret in the Azure key vault.
type secretAttribute struct {
	Name    string // Name of the secret.
	Version string // Version of the secret.
}

// interpAttr is a sensitive attribute interpolated from somewhere.
type interpAttr struct {
	Type      string `json: "type"`      // Type of resource.
	ID        string `json: "id"`        // ID of the resource.
	Attribute string `json: "attribute"` // Attribute name of resource.
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

// State reads the state from the memory.
func (s *State) State() *terraform.State {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.state.DeepCopy()
}

// WriteState writes the new state to memory.
func (s *State) WriteState(ts *terraform.State) error {
	// Lock, yay!
	s.mu.Lock()
	defer s.mu.Unlock()

	// Check if the upper-level hasn't forgotten to report sensitive attributes.
	for _, module := range s.modules {
		if module.Path == nil || module.Resources == nil {
			return errors.New("no reported sensitive attributes")
		}
	}

	// Check if the new written state has the same lineage as the old previous one.
	if s.readState != nil && !ts.SameLineage(s.readState) {
		// don't err here!
		log.Printf("[WARN] incompatible state lineage: given %s but want %s", ts.Lineage, s.readState.Lineage)
	}

	// Write the state to memory.
	s.state = ts.DeepCopy()
	if s.readState != nil {
		// Fix serial if someone wrote an incorrect serial in the state.
		s.state.Serial = s.readState.Serial
		// Serial is *only* increased when state is persisted.
	}

	return nil
}

// RefreshState fetches the state from the blob.
func (s *State) RefreshState() error {
	// Lock, milady.
	s.mu.Lock()
	defer s.mu.Unlock()

	// Get state data from the blob.
	payload, err := s.Blob.Get()
	if err != nil {
		return fmt.Errorf("error getting state from the blob: %s", err)
	}
	// Check if there is no data in the blob.
	if payload == nil {
		// Sync in-memory state with the empty blob.
		s.state = nil
		s.readState = nil
		// Indicate that the blob contains no state.
		return nil
	}

	// Unmask remote state.
	var m map[string]interface{}
	if err := json.Unmarshal(payload.Data, &m); err != nil {
		return fmt.Errorf("error unmarshalling state to map: %s", err)
	}
	/*
		iter(m, func(string, name string, value *interface{}) {
			fmt.Printf("%s: %v\n", name, *value)
		})
	*/

	// Convert it back to terraform.State.
	j, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("error marshalling map to JSON: %s", err)
	}
	var terraState terraform.State
	if err := json.Unmarshal(j, &terraState); err != nil {
		return fmt.Errorf("error unmarshalling JSON to terraform.State: %s", err)
	}

	// Read the state data into memory.
	state, err := terraform.ReadState(bytes.NewReader(payload.Data))
	if err != nil {
		return err
	}
	s.state = state
	// Make a copy used to track changes.
	s.readState = s.state.DeepCopy()
	return nil
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
func (s *State) maskModule(moduleIndex int, module map[string]interface{}) {
	for resourceName, resource := range module["resources"].(map[string]interface{}) {
		fmt.Printf("%s:\n", resourceName)
		r := resource.(map[string]interface{})
		primary := r["primary"].(map[string]interface{})
		s.maskResource(resourceName, moduleIndex, primary["attributes"].(map[string]interface{}))
	}
}

// maskResource masks all sensitive attributes in a resource.
func (s *State) maskResource(resourceName string, moduleIndex int, attributes map[string]interface{}) {
	for name, value := range attributes {
		if s.modules[moduleIndex].Resources[resourceName][name] {
			attributes[name] = secretAttribute{
				Name:    "NameTest",
				Version: "VerTest",
			}
			fmt.Printf("  %s: %v\n", name, value)
		}
	}
}

// PersistState saves the in-memory state to the blob.
func (s *State) PersistState() error {
	// Lock, harr!
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.state == nil {
		return errors.New("state is empty")
	}

	// Check for any changes to the in-memory state.
	if !s.state.MarshalEqual(s.readState) {
		s.state.Serial++
	}

	// TODO: Mask sensitive attributes.
	data, err := json.Marshal(s.state)
	if err != nil {
		return fmt.Errorf("error marshalling state: %s", err)
	}
	m := make(map[string]interface{})
	json.Unmarshal(data, &m)
	for i, module := range m["modules"].([]interface{}) {
		mod := module.(map[string]interface{})
		if pathEqual(mod["path"].([]interface{}), s.modules[i].Path) {
			s.maskModule(i, mod)
		}
	}
	fmt.Printf("%v\n", m)

	data, err = json.Marshal(m)
	if err != nil {
		return fmt.Errorf("error marshalling map: %s", err)
	}

	// Put the current in-memory state in blob.
	var buf bytes.Buffer
	if err := terraform.WriteState(s.state, &buf); err != nil {
		return err
	}
	err = s.Blob.Put(buf.Bytes())
	if err != nil {
		return fmt.Errorf("error leasing and putting buffer: %s", err)
	}

	// Set the persisted state as our new main reference state.
	s.readState = s.state.DeepCopy()
	return nil
}

// Lock locks the state.
func (s *State) Lock(info *state.LockInfo) (string, error) {
	return s.Blob.Lock(info)
}

// Unlock unlocks the state.
func (s *State) Unlock(id string) error {
	return s.Blob.Unlock(id)
}

// Report is used to report sensitive attributes to the state.
func (s *State) Report(modules []*terraform.ModuleDiff) {
	// Lock!
	s.mu.Lock()
	defer s.mu.Unlock()

	// Report sensitive attributes.
	if len(s.modules) != len(modules) {
		s.modules = make([]Module, len(modules))
	}
	for i, module := range modules {
		s.modules[i].Path = make([]string, len(module.Path))
		copy(s.modules[i].Path, module.Path)
		s.modules[i].Resources = make(map[string]map[string]bool)
		for resourceName, resourceValue := range module.Resources {
			s.modules[i].Resources[resourceName] = make(map[string]bool)
			for attrName, attrValue := range resourceValue.Attributes {
				s.modules[i].Resources[resourceName][attrName] = attrValue.Sensitive
			}
		}
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
