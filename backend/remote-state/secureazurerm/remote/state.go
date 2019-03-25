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

	Blob *blob.Blob

	state, // in-memory state.
	readState *terraform.State // state read from the blob.

	module Module
}

// Module is used to report which attributes are sensitive or not.
type Module struct {
	Path      []string
	Resources map[string]map[string]bool
}

// secretAttr is a sensitive attribute that is located as a secret in the Azure key vault.
type secretAttr struct {
	Name    string // Name of the secret.
	Version string // Version of the secret.
}

// interpAttr is a sensitive attribute interpolated from somewhere.
type interpAttr struct {
	Type      string `json: type`      // Type of resource.
	ID        string `json: id`        // ID of the resource.
	Attribute string `json: attribute` // Attribute name of resource.
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
	return "", fmt.Errorf("error unmaski)ng attributes")
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

	if s.module.Path == nil || s.module.Resources == nil {
		return errors.New("no reported sensitive attributes")
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
		panic(err)
	}

	// Convert it back to terraform.State.
	j, err := json.Marshal(m)
	if err != nil {
		panic(err)
	}
	var terraState terraform.State
	if err := json.Unmarshal(j, &terraState); err != nil {
		panic(err)
	}

	// Read the state data into memory.
	state, err := terraform.ReadState(bytes.NewReader(payload.Data))
	if err != nil {
		return err
	}
	s.state = state
	// Make a copy used in comparison to track changes.
	s.readState = s.state.DeepCopy()
	return nil
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

	data, err := json.Marshal(s.state)
	if err != nil {
		return fmt.Errorf("error marshalling state: %s", err)
	}
	m := make(map[string]interface{})
	json.Unmarshal(data, &m)
	// TODO: Turn sensitive to JSON objects.
	data, err = json.Marshal(m)
	if err != nil {
		return fmt.Errorf("error marshalling map: %s", err)
	}

	// Put the current in-memory state in blob.
	var buf bytes.Buffer
	if err := terraform.WriteState(s.state, &buf); err != nil {
		return err
	}
	err = s.Blob.LeasePut(buf.Bytes())
	if err != nil {
		return err
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

// Report is used to report sensitive attributes.
func (s *State) Report(modules []*terraform.ModuleDiff) {
	// Lock!
	s.mu.Lock()
	defer s.mu.Unlock()

	// Report sensitive attributes.
	moduleDiffs := []Module{}
	for _, mod := range modules {
		md := Module{Resources: make(map[string]map[string]bool)}
		copy(md.Path, mod.Path)
		moduleDiffs = append(moduleDiffs, md)
		for key, r := range mod.Resources {
			//md.Resources[key] = r.CopyAttributes()
			fmt.Printf("%s\n", key, r)
		}
	}
	// DEBUG: Print which attributes are sensitive. ~ bao.
	for _, md := range moduleDiffs {
		for name, r := range md.Resources {
			fmt.Printf("%s:\n", name)
			for attr, value := range r {
				fmt.Printf("  %s: %t\n", attr, value)
			}
		}
	}
}
