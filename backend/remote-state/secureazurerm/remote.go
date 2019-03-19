package secureazurerm

import (
	"encoding/json"
	"errors"
	"sync"

	"github.com/hashicorp/terraform/terraform"
)

// RemoteState contains the remote state.
type RemoteState struct {
	mu          sync.Mutex
	Client      *Client
	state       *terraform.State
	maskedState map[string]interface{}
}

// State reads the remote state.
func (s *RemoteState) Read() *terraform.State {
	s.mu.Lock()
	defer s.mu.Unlock()

	var remoteState []byte
	err := json.Unmarshal(remoteState, s.maskedState)
	if err != nil {
		panic(err)
	}

	// TODO: Unmask remote state.

	b, err := json.Marshal(s.maskedState)
	if err != nil {
		panic(err)
	}
	if err := json.Unmarshal(b, s.state); err != nil {
		panic(err)
	}

	return s.state.DeepCopy()
}

// WriteState writes the remote state.
func (s *RemoteState) Write() error {
	return errors.New("todo")
}
