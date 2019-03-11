package secureazurerm

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"

	"github.com/Azure/azure-sdk-for-go/storage"
	"github.com/hashicorp/go-multierror"
	"github.com/hashicorp/terraform/state"
	"github.com/hashicorp/terraform/state/remote"
	"github.com/hashicorp/terraform/terraform"
)

// Client holds the state to communicate with Azure Resource Manager.
type Client struct {
	// Client to operate on Azure Storage Account:
	blobClient    storage.BlobStorageClient // Client to communicate with Azure Resource Manager to operate on Azure Storage Accounts.
	containerName string                    // The name of the container that contains the blob storing the remote state in JSON.
	blobName      string                    // The name of the blob that stores the remote state in JSON. Should be equal to workspace-name.
	leaseID       string                    // The lease ID used as a lock/mutex to the blob.
}

// Get gets the remote state from the blob in the container in the Azure Storage Account.
func (c *Client) Get() (*remote.Payload, error) {
	// Get blob containing remote state.
	blob := c.blobClient.GetContainerReference(c.containerName).GetBlobReference(c.blobName)
	options := &storage.GetBlobOptions{}
	if c.leaseID != "" {
		options.LeaseID = c.leaseID
	}

	// Check if blob exists.
	blobExists, err := blob.Exists()
	if err != nil {
		return nil, err
	}
	// Create blob if it does not exists.
	if !blobExists {
		blob.CreateBlockBlob(&storage.PutBlobOptions{})
	}

	// Get remote state from blob.
	data, err := blob.Get(options)
	if err != nil {
		if storErr, ok := err.(storage.AzureStorageServiceError); ok {
			return nil, fmt.Errorf(storErr.Code)
		}
		return nil, err
	}
	defer data.Close() // TODO: Handle close error.

	// Copy the blob data to a byte buffer.
	buf := bytes.NewBuffer(nil)
	if _, err := io.Copy(buf, data); err != nil {
		return nil, fmt.Errorf("failed to read remote state: %s", err)
	}
	// Make payload from remote state blob data.
	payload := &remote.Payload{Data: buf.Bytes()}
	if len(payload.Data) == 0 { // is payload empty?
		return nil, nil
	}
	return payload, nil
}

// Put puts the remote state data into a blob and the key vault.
func (c *Client) Put(data []byte) error {
	// Check if the remote state blob to work on has been set.
	if c.blobName == "" {
		return fmt.Errorf("blob name is empty")
	}
	// Check if no lease has been acquired.
	if c.leaseID == "" {
		return fmt.Errorf("no lease has been acquired")
	}

	// Get reference to remote state blob.
	blob := c.blobClient.GetContainerReference(c.containerName).GetBlobReference(c.blobName)
	// Set blob content type, which is JSON.
	blob.Properties.ContentType = "application/json"
	// Set blob content length.
	blob.Properties.ContentLength = int64(len(data))

	// Check if blob exists.
	blobExists, err := blob.Exists()
	if err != nil { // failed to check existence of blob.
		return err
	}
	if blobExists {
		// Create a new snapshot of the existing remote state blob.
		blob.CreateSnapshot(&storage.SnapshotOptions{})

		// ??
		if err = blob.GetMetadata(&storage.GetBlobMetadataOptions{LeaseID: c.leaseID}); err != nil {
			return err
		}
	}

	// Create a block blob and upload the remote state in JSON to the blob.
	if err = blob.CreateBlockBlobFromReader(bytes.NewReader(data), &storage.PutBlobOptions{LeaseID: c.leaseID}); err != nil {
		return err
	}
	return blob.SetProperties(&storage.SetBlobPropertiesOptions{LeaseID: c.leaseID})
}

// Delete deletes blob that contains the blob state.
func (c *Client) Delete() error {
	// Get blob from container in storage account.
	blob := c.blobClient.GetContainerReference(c.containerName).GetBlobReference(c.blobName)

	// Delete blob.
	options := &storage.DeleteBlobOptions{} // Set delete blob options.
	if c.leaseID != "" {
		options.LeaseID = c.leaseID
	}
	return blob.Delete(options) // Call the API to delete it!
}

// Lock acquires the lease of the remote state blob.
func (c *Client) Lock(info *state.LockInfo) (string, error) {
	info.Path = fmt.Sprintf("%s/%s", c.containerName, c.blobName)

	blob := c.blobClient.GetContainerReference(c.containerName).GetBlobReference(c.blobName)
	var err error
	info.ID, err = blob.AcquireLease(-1, info.ID, &storage.LeaseOptions{})
	// If failed to acquire lease.
	if err != nil {
		getLockInfoErr := func(err error) error {
			lockInfo, infoErr := c.getLockInfo()
			if infoErr != nil {
				err = multierror.Append(err, infoErr)
			}
			return &state.LockError{
				Err:  err,
				Info: lockInfo,
			}
		}

		if storErr, ok := err.(storage.AzureStorageServiceError); ok && storErr.Code != "BlobNotFound" {
			return "", getLockInfoErr(err)
		}

		// failed to lock as there was no state blob, thus write empty state.
		remoteState := &remote.State{Client: c}

		// ensure state is actually empty
		if err := remoteState.RefreshState(); err != nil {
			return "", fmt.Errorf("failed to refresh state before writing empty state for locking: %s", err)
		}

		if v := remoteState.State(); v == nil {
			if err := remoteState.WriteState(terraform.NewState()); err != nil {
				return "", fmt.Errorf("failed to write empty state for locking: %s", err)
			}
			if err := remoteState.PersistState(); err != nil {
				return "", fmt.Errorf("failed to persist empty state for locking: %s", err)
			}
		}

		// Acquire lease on blob.
		info.ID, err = blob.AcquireLease(-1, info.ID, &storage.LeaseOptions{})
		if err != nil {
			return "", getLockInfoErr(err)
		}
	}
	c.leaseID = info.ID

	if err = c.writeLockInfo(info); err != nil {
		return "", err
	}

	return info.ID, nil
}

// Unlock unlocks the mutex of remote state.
func (c *Client) Unlock(id string) error {
	lockErr := &state.LockError{}

	lockInfo, err := c.getLockInfo()
	if err != nil {
		lockErr.Err = fmt.Errorf("failed to retrieve lock info: %s", err)
		return lockErr
	}
	lockErr.Info = lockInfo

	if lockInfo.ID != id {
		lockErr.Err = fmt.Errorf("lock id %q does not match existing lock", id)
		return lockErr
	}

	if err := c.writeLockInfo(nil); err != nil {
		lockErr.Err = fmt.Errorf("failed to delete lock info from metadata: %s", err)
		return lockErr
	}

	blob := c.blobClient.GetContainerReference(c.containerName).GetBlobReference(c.blobName)
	if err = blob.ReleaseLease(id, &storage.LeaseOptions{}); err != nil {
		lockErr.Err = err
		return lockErr
	}
	c.leaseID = ""
	return nil
}

const (
	lockInfoMetaKey = "terraformlockid" // Must be lower case!
)

// getLockInfo retrieves lock info from metadata.
func (c *Client) getLockInfo() (*state.LockInfo, error) {
	blob := c.blobClient.GetContainerReference(c.containerName).GetBlobReference(c.blobName)

	if err := blob.GetMetadata(&storage.GetBlobMetadataOptions{}); err != nil {
		return nil, err
	}

	raw := blob.Metadata[lockInfoMetaKey]
	if raw == "" {
		return nil, fmt.Errorf("blob metadata %q was empty", lockInfoMetaKey)
	}

	data, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return nil, err
	}

	lockInfo := &state.LockInfo{}
	if err = json.Unmarshal(data, lockInfo); err != nil {
		return nil, err
	}

	return lockInfo, nil
}

// writeLockInfo writes lock info in base64 to blob metadata, and deletes metadata entry if info is nil.
func (c *Client) writeLockInfo(info *state.LockInfo) error {
	blob := c.blobClient.GetContainerReference(c.containerName).GetBlobReference(c.blobName)
	if err := blob.GetMetadata(&storage.GetBlobMetadataOptions{LeaseID: c.leaseID}); err != nil {
		return err
	}
	if info == nil {
		delete(blob.Metadata, lockInfoMetaKey)
	} else {
		blob.Metadata[lockInfoMetaKey] = base64.StdEncoding.EncodeToString(info.Marshal())
	}
	return blob.SetMetadata(&storage.SetBlobMetadataOptions{LeaseID: c.leaseID})
}
