package secureazurerm

import (
	"fmt"
	"sort"

	"github.com/hashicorp/terraform/backend/remote-state/secureazurerm/remote/account/blob"
	"github.com/hashicorp/terraform/state"
	"github.com/hashicorp/terraform/terraform"
)

// States returns a list of the names of all remote states stored in separate unique blob.
// They are all named after the workspace.
// Basically, remote state = workspace = blob.
func (b *Backend) States() ([]string, error) {
	// Get the blobs of the container.
	blobs, err := b.container.List()
	if err != nil {
		return nil, err
	}
	// List workspaces (which is equivalent to blobs) in the container.
	workspaces := []string{}
	for _, blob := range blobs {
		workspaces = append(workspaces, blob.Name)
	}
	sort.Strings(workspaces[1:]) // default is placed first in the returned list.
	return workspaces, nil
}

// DeleteState deletes remote state.
func (b *Backend) DeleteState(name string) error {
	blob, err := blob.Setup(&b.container, name, nil) // blob name = workspace name.
	if err != nil {
		return err
	}
	if err := blob.Delete(); err != nil {
		return fmt.Errorf("error deleting state %s: %s", name, err)
	}
	return nil
}

// State returns the state specified by name.
func (b *Backend) State(name string) (state.State, error) {
	s := &blob.State{Client: c}
	blob := blob.Setup(&b.container, name, func(b *blob.Blob) error {
		// Create new state in-memory.
		if err := s.WriteState(terraform.NewState()); err != nil {
			return fmt.Errorf("error creating new state in-memory: %s", err)
		}
		// Write that in-memory state to remote state.
		if err := s.PersistState(); err != nil {
			return fmt.Errorf("error writing in-memory state to remote: %s", err)
		}
	})
	return s, nil
}
