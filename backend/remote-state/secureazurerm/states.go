package secureazurerm

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform/backend/remote-state/secureazurerm/remote"
	"github.com/hashicorp/terraform/backend/remote-state/secureazurerm/remote/account/blob"
	"github.com/hashicorp/terraform/backend/remote-state/secureazurerm/remote/keyvault"
	"github.com/hashicorp/terraform/state"
)

// States returns the name of all blobs that stores the state file.
// They're all named after the workspace (workspace name = blob name).
func (b *Backend) States() ([]string, error) {
	// Get the blobs of the container.
	blobs, err := b.container.List()
	if err != nil {
		return nil, fmt.Errorf("error listing blobs: %s", err)
	}

	// List workspaces (which is equivalent to blobs) in the container.
	workspaces := []string{}
	for _, blob := range blobs {
		workspaces = append(workspaces, blob.Name)
	}

	return workspaces, nil
}

// DeleteState deletes remote state.
func (b *Backend) DeleteState(name string) error {
	// Setup state blob.
	blob, err := blob.Setup(b.container, name) // blob name = workspace name.
	if err != nil {
		return fmt.Errorf("error setting up state blob: %s", err)
	}

	// Setup the state's key vault.
	keyVault, err := b.setupKeyVault(blob, name)
	if err != nil {
		return fmt.Errorf("error setting up state key vault: %s", err)
	}
	// and then delete the key vault!
	keyVault.Delete(context.Background())
	if err := blob.Delete(); err != nil {
		return fmt.Errorf("error deleting state %s: %s", name, err)
	}

	return nil
}

// State returns the state of the given workspaceName.
func (b *Backend) State(workspaceName string) (state.State, error) {
	// Setup blob.
	blob, err := blob.Setup(b.container, workspaceName)
	if err != nil {
		return nil, fmt.Errorf("error setting up state blob: %s", err)
	}

	// Setup key vault.
	keyVault, err := b.setupKeyVault(workspaceName)
	if err != nil {
		return nil, fmt.Errorf("error setting up state key vault: %s", err)
	}

	return &remote.State{Blob: blob, KeyVault: keyVault, Props: &b.props}, nil
}

// setupKeyVault setups the state/workspace's key vault.
func (b *Backend) setupKeyVault(workspaceName string) (*keyvault.KeyVault, error) {
	keyVault, err := keyvault.Setup(context.Background(), &b.props, workspaceName)
	if err != nil {
		return nil, fmt.Errorf("error setting up key vault: %s", err)
	}
	return keyVault, nil
}
