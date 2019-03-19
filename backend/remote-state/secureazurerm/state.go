package secureazurerm

import (
	"errors"
	"fmt"
	"sort"

	"github.com/Azure/azure-sdk-for-go/storage"
	"github.com/hashicorp/terraform/backend"
	"github.com/hashicorp/terraform/backend/remote-state/secureazurerm/remote/comm"
	"github.com/hashicorp/terraform/state"
	"github.com/hashicorp/terraform/state/remote"
	"github.com/hashicorp/terraform/terraform"
)

const containerNameNotSetErrorMsg = "container name is not set"

// States returns a list of the names of all remote states stored in separate unique blob.
// They are all named after the workspace.
// Basically, remote state = workspace = blob.
func (b *Backend) States() ([]string, error) {
	if b.containerName == "" {
		return nil, errors.New(containerNameNotSetErrorMsg)
	}

	// Get blobs of container.
	r, err := b.blobClient.GetContainerReference(b.containerName).ListBlobs(storage.ListBlobsParameters{})
	if err != nil {
		return nil, err
	}

	// List workspaces (which is equivalent to blobs) in the container.
	workspaces := []string{}
	for _, blob := range r.Blobs {
		workspaces = append(workspaces, blob.Name)
	}
	sort.Strings(workspaces[1:]) // default is placed first in the returned list.
	return workspaces, nil
}

// DeleteState deletes remote state.
func (b *Backend) DeleteState(name string) error {
	if b.containerName == "" {
		return errors.New(containerNameNotSetErrorMsg)
	}

	if name == backend.DefaultStateName {
		return errors.New("can't delete default state")
	}
	c := &Client{
		blobClient:    b.blobClient,
		containerName: b.containerName,
		blobName:      name, // workspace name.
	}
	lockInfo := state.NewLockInfo()
	lockInfo.Operation = "DeleteState"
	leaseID, err := c.Lock(lockInfo)
	if err != nil {
		return fmt.Errorf("error locking blob: %s", err)
	}
	if err = c.Delete(); err != nil {
		if err := c.Unlock(leaseID); err != nil {
			return fmt.Errorf("error unlocking blob (may need to be manually broken): %s", err)
		}
		return fmt.Errorf("error deleting blob: %s", err)
	}
	return nil
}

// State returns remote state specified by name.
func (b *Backend) State(name string) (state.State, error) {
	if b.containerName == "" {
		return nil, errors.New(containerNameNotSetErrorMsg)
	}

	c := &comm.Client{
		BlobClient:    b.blobClient,
		ContainerName: b.containerName,
		BlobName:      name, // workspace name.
	}
	s := &remote.State{Client: c}

	// Check if blob exists.
	exists, err := c.Exists()
	if err != nil {
		return nil, fmt.Errorf("error checking blob existence: %s", err)
	}
	// If not exists, write empty state blob (no need for lock when the blob does not exists).
	if !exists {
		// Create new state in-memory.
		if err := s.WriteState(terraform.NewState()); err != nil {
			return nil, fmt.Errorf("error creating new state in-memory: %s", err)
		}
		// Write that in-memory state to remote state.
		if err := s.PersistState(); err != nil {
			return nil, fmt.Errorf("error writing in-memory state to remote: %s", err)
		}
	}

	return s, nil
}
