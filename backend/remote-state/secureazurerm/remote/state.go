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
