package secureazurerm

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform/backend/remote-state/secureazurerm/remote"
	"github.com/hashicorp/terraform/backend/remote-state/secureazurerm/remote/account/blob"
	"github.com/hashicorp/terraform/backend/remote-state/secureazurerm/remote/keyvault"
	"github.com/hashicorp/terraform/state"
)

// States returns a list of the names of all remote states stored in separate unique blob.
// They are all named after the workspace.
// Basically, remote state = workspace = blob.
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
	if err := blob.Delete(); err != nil {
		return fmt.Errorf("error deleting state %s: %s", name, err)
	}

	// Setup state key vault.
	keyVault, err := b.setupKeyVault(name)
	if err != nil {
		return fmt.Errorf("error setting up state key vault: %s", err)
	}
	keyVault.Delete(context.Background())

	return nil
}

// State returns the state specified by workspace.
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

	return &remote.State{Blob: blob, KeyVault: keyVault}, nil
}

// setupKeyVault setups the state key vault.
func (b *Backend) setupKeyVault(workspaceName string) (*keyvault.KeyVault, error) {
	keyVault, err := keyvault.Setup(context.Background(), b.props, workspaceName)
	if err != nil {
		return nil, fmt.Errorf("error setting up key vault: %s", err)
	}
	return &keyVault, nil
}
