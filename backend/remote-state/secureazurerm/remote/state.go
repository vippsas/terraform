package remote

import (
	"encoding/json"
	"fmt"
	"sync"

	"github.com/hashicorp/terraform/backend/remote-state/secureazurerm/remote/account/blob"
	"github.com/hashicorp/terraform/terraform"
)

// State contains the remote state.
type State struct {
	mu   sync.Mutex
	blob *blob.Blob
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

// Read reads the state from the remote blob.
func (s *State) Read() *terraform.State {
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
