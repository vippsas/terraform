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
	state.State
	mu sync.Mutex

	blob *blob.Blob

	state, // in-memory state.
	readState *terraform.State // state read from the blob.
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

// WriteState writes the new state to memory.
func (s *State) WriteState(s *terraform.State) error {
	// Lock, yay!
	s.mu.Lock()
	defer s.mu.Unlock()

	// Check if the new written state has the same lineage as the old previous one.
	if s.readState != nil && !state.SameLineage(s.readState) {
		// don't err here!
		log.Printf("[WARN] incompatible state lineage: given %s but want %s", state.Lineage, s.readState.Lineage)
	}

	// Write the state to memory.
	s.state = state.DeepCopy()

	if s.readState != nil {
		// Set if someone wrote an incorrect serial.
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

	// Get state from the blob.
	payload, err := s.blob.Get()
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
	state, err := terraform.ReadState(bytes.NewReader(payload.Data))
	if err != nil {
		return err
	}
	s.state = state
	s.readState = s.state.DeepCopy() // to track changes.
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

	// Put the current in-memory state in blob.
	var buf bytes.Buffer
	if err := terraform.WriteState(s.state, &buf); err != nil {
		return err
	}
	err := s.Client.Put(buf.Bytes())
	if err != nil {
		return err
	}

	// Set the persisted state as our new main reference state.
	s.readState = s.state.DeepCopy()
	return nil
}

// Lock locks the state.
func (s *State) Lock(info *LockInfo) (string, error) {
	return blob.Lock()
}

// Unlock unlocks the state.
func (s *State) Unlock(id string) error {
	return blob.Unlock(id)
}

// State reads the state from the remote blob.
func (s *State) State() *terraform.State {
	// Lock.
	s.mu.Lock()
	defer s.mu.Unlock()

	// Get remote state data from blob storage.
	payload, err := s.blob.Get()
	if err != nil {
		panic(err)
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
	return &terraState
}

// Attr is a resource attribute.
type Attr struct {
	Value     string
	Sensitive bool
}

type Module struct {
	Path      []string
	Resources map[string]map[string]Attr
}

// Write writes Terraform's state to the remote blob.
func (s *State) Write(state *terraform.State, md *Module) error {
	// Lock.
	s.mu.Lock()
	defer s.mu.Unlock()

	bytes, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("error marshalling state: %s", err)
	}
	m := make(map[string]interface{})
	json.Unmarshal(bytes, &m)
	// TODO: Turn sensitive to JSON objects.
	bytes, err = json.Marshal(m)
	if err != nil {
		return fmt.Errorf("error marshalling map: %s", err)
	}
	return nil
}
