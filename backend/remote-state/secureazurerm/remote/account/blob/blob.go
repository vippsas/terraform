package blob

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"

	"github.com/Azure/azure-sdk-for-go/storage"
	"github.com/hashicorp/go-uuid"
	"github.com/hashicorp/terraform/backend/remote-state/secureazurerm/common"
	"github.com/hashicorp/terraform/backend/remote-state/secureazurerm/remote/account"
	"github.com/hashicorp/terraform/state"
	"github.com/hashicorp/terraform/state/remote"
	"github.com/hashicorp/terraform/version"
)

// Blob communicates to the remote blob in a container in a storage account in Azure.
type Blob struct {
	// Client to operate on Azure Storage Account:
	container *account.Container

	// Blob info:
	Name    string // The name of the blob that stores the remote state in JSON. Should be equal to workspace-name.
	leaseID string // The lease ID used as a lock/mutex to the blob.
}

// Setup setups a new or existing blob.
func Setup(container *account.Container, name string) (*Blob, error) {
	// Initialize blob.
	blob := Blob{
		container: container,
		Name:      name,
	}

	// Check if blob exists.
	exists, err := blob.Exists()
	if err != nil {
		return nil, fmt.Errorf("error checking blob existence: %s", err)
	}
	// If not exists, write empty state blob.
	if !exists {
		lineage, err := uuid.GenerateUUID()
		if err != nil {
			return nil, fmt.Errorf("error generating initial lineage: %s", err)
		}
		state := common.SecureState{
			Version:          "1",
			TerraformVersion: version.Version,
			Lineage:          lineage,
			Serial:           0,
		}
		b, err := json.MarshalIndent(&state, "", "  ")
		if err != nil {
			return nil, fmt.Errorf("error marshalling secure state to JSON: %s", err)
		}
		if err := blob.Put(b); err != nil {
			return nil, fmt.Errorf("error writing buffer to state blob: %s", err)
		}
	}

	return &blob, nil
}

// Exists check if remote state blob exists already.
func (b *Blob) Exists() (blobExists bool, err error) {
	// Check if blob exists.
	blobExists, err = b.container.GetBlob(b.Name).Exists()
	if err != nil {
		return false, err // failed to check if blob exists.
	}
	return
}

// Get gets the remote state from the blob in the container in the Azure Storage Account.
func (b *Blob) Get() (payload *remote.Payload, returnErr error) {
	// Check if client's fields are set correctly.
	if err := b.isValid(); err != nil {
		return nil, fmt.Errorf("blob is invalid: %s", err)
	}

	// Get blob containing remote state.
	blob := b.container.GetBlob(b.Name)

	// Check if blob exists.
	blobExists, err := blob.Exists()
	if err != nil {
		return nil, err // failed to check if blob exists.
	}
	if !blobExists {
		return nil, fmt.Errorf("blob does not exist")
	}

	// Get remote state from blob.
	data, err := blob.Get(&storage.GetBlobOptions{})
	if err != nil {
		if storErr, ok := err.(storage.AzureStorageServiceError); ok {
			return nil, fmt.Errorf(storErr.Code)
		}
		return nil, err
	}
	defer func() {
		if err := data.Close(); err != nil {
			returnErr = fmt.Errorf("error closing blob: %s", err)
			return
		}
	}()

	// Copy the blob data to a byte buffer.
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, data); err != nil {
		return nil, fmt.Errorf("failed to read remote state: %s", err)
	}
	// Make payload from remote state blob data.
	payload = &remote.Payload{Data: buf.Bytes()}
	if len(payload.Data) == 0 {
		return nil, nil
	}
	return payload, nil
}

// Put puts data into the blob.
func (b *Blob) Put(data []byte) error {
	// Check if client's fields are set correctly.
	if err := b.isValid(); err != nil {
		return fmt.Errorf("blob is invalid: %s", err)
	}
	// Get blob reference to the remote blob in the container in the storage account.
	blob := b.container.GetBlob(b.Name)

	// Check if blob exists.
	blobExists, err := blob.Exists()
	if err != nil {
		return fmt.Errorf("error checking existence of blob: %s", err)
	}
	if blobExists {
		if err := b.isLeased(); err != nil {
			return fmt.Errorf("no lease on blob: %s", err)
		}
		// Create a new snapshot of the existing remote state blob.
		blob.CreateSnapshot(&storage.SnapshotOptions{})
		// Get the existing blob's metadata, which will be re-used in the new block blob that replaces the old one.
		if err := blob.GetMetadata(&storage.GetBlobMetadataOptions{LeaseID: b.leaseID}); err != nil {
			return fmt.Errorf("error getting metadata: %s", err)
		}
	}

	// Set the blob's properties.
	blob.Properties.ContentType = "application/json"
	blob.Properties.ContentLength = int64(len(data))

	// Create a block blob that replaces the old one and upload the remote state in JSON to the blob.
	if err = blob.CreateBlockBlobFromReader(bytes.NewReader(data), &storage.PutBlobOptions{LeaseID: b.leaseID}); err != nil {
		return fmt.Errorf("error creating block blob: %s", err)
	}
	return blob.SetProperties(&storage.SetBlobPropertiesOptions{LeaseID: b.leaseID})
}

// Delete deletes the blob.
func (b *Blob) Delete() (returnErr error) {
	// Is fields set correctly?
	if err := b.isValid(); err != nil {
		return fmt.Errorf("blob is invalid: %s", err)
	}
	// Lock/Lease blob.
	lockInfo := state.NewLockInfo()
	lockInfo.Operation = "DeleteState"
	if _, err := b.Lock(lockInfo); err != nil {
		return fmt.Errorf("error locking blob: %s", err)
	}

	// Call the API to delete the blob!
	del := true
	if err := b.container.GetBlob(b.Name).Delete(&storage.DeleteBlobOptions{LeaseID: b.leaseID, DeleteSnapshots: &del}); err != nil {
		return fmt.Errorf("error deleting blob: %s", err)
	}
	return nil
}

// Lock acquires the lease of the blob.
func (b *Blob) Lock(info *state.LockInfo) (string, error) {
	// Check if blob is valid.
	if err := b.isValid(); err != nil {
		return "", fmt.Errorf("blob is invalid: %s", err)
	}

	// Acquire lease on blob.
	leaseID, err := b.container.GetBlob(b.Name).AcquireLease(-1, info.ID, &storage.LeaseOptions{})
	if err != nil {
		return "", fmt.Errorf("error acquiring lease: %s", err)
	}
	info.ID = leaseID
	b.leaseID = info.ID

	// Write info about Terraform's lock into the blob's metadata.
	if err := b.writeLockInfo(info); err != nil {
		return "", fmt.Errorf("error writing lock info: %s", err)
	}

	// Return the path and ID to the blob.
	info.Path = fmt.Sprintf("%s/%s", b.container.Name, b.Name)
	return info.ID, nil
}

// Unlock breaks the lease of the blob.
func (b *Blob) Unlock(id string) error {
	if err := b.isValid(); err != nil {
		return fmt.Errorf("blob is invalid: %s", err)
	}

	lockErr := &state.LockError{}
	lockInfo, err := b.readLockInfo()
	if err != nil {
		lockErr.Err = fmt.Errorf("error retrieving lock info: %s", err)
		return lockErr
	}
	lockErr.Info = lockInfo

	if lockInfo.ID != id {
		lockErr.Err = fmt.Errorf("lock id %q does not match existing lock", id)
		return lockErr
	}

	if err := b.writeLockInfo(nil); err != nil {
		lockErr.Err = fmt.Errorf("error deleting lock info from metadata: %s", err)
		return lockErr
	}

	if err = b.container.GetBlob(b.Name).ReleaseLease(id, &storage.LeaseOptions{}); err != nil {
		lockErr.Err = err
		return lockErr
	}
	b.leaseID = "" // set to "no lease acquired".
	return nil
}

// IsValid checks if the client's fields are set correctly before using it.
func (b *Blob) isValid() error {
	// Check if the container that contains the blob has been set.
	if b.container.Name == "" {
		return fmt.Errorf("container name is empty")
	}
	// Check if the remote state blob to work on has been set.
	if b.Name == "" {
		return fmt.Errorf("blob name is empty")
	}
	return nil
}

// isLeased checks if a lease has been acquired on blob.
func (b *Blob) isLeased() error {
	// Check if no lease has been acquired.
	if b.leaseID == "" {
		return fmt.Errorf("no lease has been acquired on blob")
	}
	return nil
}
