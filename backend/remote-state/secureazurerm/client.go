package secureazurerm

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"

	"github.com/Azure/azure-sdk-for-go/storage"
	"github.com/hashicorp/terraform/state"
	"github.com/hashicorp/terraform/state/remote"
)

// Client communicates with Azure.
type Client struct {
	// Client to operate on Azure Storage Account:
	blobClient    storage.BlobStorageClient // Client to communicate with Azure Resource Manager to operate on Azure Storage Accounts.
	containerName string                    // The name of the container that contains the blob storing the remote state in JSON.
	blobName      string                    // The name of the blob that stores the remote state in JSON. Should be equal to workspace-name.
	leaseID       string                    // The lease ID used as a lock/mutex to the blob.
}

// Exists check if remote state blob exists already.
func (c *Client) Exists() (bool, error) {
	// Check if blob exists.
	blobExists, err := c.getBlobRef().Exists()
	if err != nil {
		return false, err // failed to check if blob exists.
	}
	return blobExists, nil
}

// Get gets the remote state from the blob in the container in the Azure Storage Account.
func (c *Client) Get() (*remote.Payload, error) {
	// Check if client's fields are set correctly.
	if err := c.isValid(); err != nil {
		return nil, fmt.Errorf("client is invalid: %s", err)
	}

	// Get blob containing remote state.
	blob := c.getBlobRef()
	options := &storage.GetBlobOptions{}
	if c.leaseID != "" {
		options.LeaseID = c.leaseID
	}

	// Check if blob exists.
	blobExists, err := blob.Exists()
	if err != nil {
		return nil, err // failed to check if blob exists.
	}
	if !blobExists {
		return nil, fmt.Errorf("blob does not exist")
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

// Put puts the remote state data into a blob.
func (c *Client) Put(data []byte) error {
	// Check if client's fields are set correctly.
	if err := c.isValid(); err != nil {
		return fmt.Errorf("client is invalid: %s", err)
	}
	// Get blob reference to the remote blob in the container in the storage account.
	blobRef := c.getBlobRef()

	// Check if blob exists.
	blobExists, err := blobRef.Exists()
	if err != nil { // failed to check existence of blob.
		return err
	}
	if blobExists {
		// Check if the blob been leased.
		if err := c.isLeased(); err != nil {
			return err
		}
		// Create a new snapshot of the existing remote state blob.
		blobRef.CreateSnapshot(&storage.SnapshotOptions{})
		// Get the existing blob's metadata, which will be re-used in the new block blob that replaces the old one.
		if err := blobRef.GetMetadata(&storage.GetBlobMetadataOptions{LeaseID: c.leaseID}); err != nil {
			return fmt.Errorf("error getting metadata: %s", err)
		}
	}
	// Set blob content type, which is JSON.
	blobRef.Properties.ContentType = "application/json"
	// Set blob content length.
	blobRef.Properties.ContentLength = int64(len(data))
	// Create a block blob that replaces the old one and upload the remote state in JSON to the blob.
	if err = blobRef.CreateBlockBlobFromReader(bytes.NewReader(data), &storage.PutBlobOptions{LeaseID: c.leaseID}); err != nil {
		return fmt.Errorf("error creating block blob: %s", err)
	}
	return blobRef.SetProperties(&storage.SetBlobPropertiesOptions{LeaseID: c.leaseID}) // if a blob existed previously, it will set the properties of it on the newly created blob.
}

// Delete deletes blob that contains the state.
func (c *Client) Delete() error {
	// Is client's fields set correctly?
	if err := c.isValid(); err != nil {
		return fmt.Errorf("client is invalid: %s", err)
	}
	// Is blob leased?
	if err := c.isLeased(); err != nil {
		return err
	}
	// Call the API to delete the blob!
	del := true
	return c.getBlobRef().Delete(&storage.DeleteBlobOptions{LeaseID: c.leaseID, DeleteSnapshots: &del})
}

// Lock acquires the lease of the blob.
func (c *Client) Lock(info *state.LockInfo) (string, error) {
	if err := c.isValid(); err != nil {
		return "", fmt.Errorf("client is invalid: %s", err)
	}

	blobRef := c.getBlobRef()
	leaseID, err := blobRef.AcquireLease(-1, info.ID, &storage.LeaseOptions{})
	if err != nil {
		return "", fmt.Errorf("error acquiring lease: %s", err)
	}
	info.ID = leaseID
	c.leaseID = info.ID

	if err := c.writeLockInfo(info); err != nil {
		return "", fmt.Errorf("error writing lock info: %s", err)
	}

	info.Path = fmt.Sprintf("%s/%s", c.containerName, c.blobName)
	return info.ID, nil
}

// Unlock breaks the lease of the blob.
func (c *Client) Unlock(id string) error {
	if err := c.isValid(); err != nil {
		return fmt.Errorf("client is invalid: %s", err)
	}

	lockErr := &state.LockError{}
	lockInfo, err := c.readLockInfo()
	if err != nil {
		lockErr.Err = fmt.Errorf("error retrieving lock info: %s", err)
		return lockErr
	}
	lockErr.Info = lockInfo

	if lockInfo.ID != id {
		lockErr.Err = fmt.Errorf("lock id %q does not match existing lock", id)
		return lockErr
	}

	if err := c.writeLockInfo(nil); err != nil {
		lockErr.Err = fmt.Errorf("error deleting lock info from metadata: %s", err)
		return lockErr
	}

	blobRef := c.getBlobRef()
	if err = blobRef.ReleaseLease(id, &storage.LeaseOptions{}); err != nil {
		lockErr.Err = err
		return lockErr
	}
	c.leaseID = "" // set to "no lease acquired".
	return nil
}

// getBlobRef returns the blob reference to the client's blob.
func (c *Client) getBlobRef() *storage.Blob {
	return c.blobClient.GetContainerReference(c.containerName).GetBlobReference(c.blobName)
}

// IsValid checks if the client's fields are set correctly before using it.
func (c *Client) isValid() error {
	// Check if the container that contains the blob has been set.
	if c.containerName == "" {
		return fmt.Errorf("container name is empty")
	}
	// Check if the remote state blob to work on has been set.
	if c.blobName == "" {
		return fmt.Errorf("blob name is empty")
	}
	return nil
}

// isLeased checks if a lease has been acquired on blob.
func (c *Client) isLeased() error {
	// Check if no lease has been acquired.
	if c.leaseID == "" {
		return fmt.Errorf("no lease has been acquired on blob")
	}
	return nil
}

const lockinfo = "lockinfo" // must be lower case!

// readLockInfo reads lockInfo from the blob's metadata.
func (c *Client) readLockInfo() (*state.LockInfo, error) {
	blobRef := c.getBlobRef()

	// Get base64-encoded lockInfo from the blob's metadata.
	if err := blobRef.GetMetadata(&storage.GetBlobMetadataOptions{}); err != nil {
		return nil, fmt.Errorf("error getting blob metadata: %s", err)
	}
	lockInfoInBase64 := blobRef.Metadata[lockinfo]
	if lockInfoInBase64 == "" {
		return nil, fmt.Errorf("blob metadata %q was empty", lockinfo)
	}

	// Decode and unmarshal back to lockInfo-struct.
	lockInfoInJSON, err := base64.StdEncoding.DecodeString(lockInfoInBase64)
	if err != nil {
		return nil, fmt.Errorf("error decoding base64: %s", err)
	}
	lockInfo := &state.LockInfo{}
	if err = json.Unmarshal(lockInfoInJSON, lockInfo); err != nil {
		return nil, fmt.Errorf("error unmarshalling lock info from JSON: %s", err)
	}

	return lockInfo, nil
}

// writeLockInfo writes lockInfo to the blob's metadata.
func (c *Client) writeLockInfo(info *state.LockInfo) error {
	blobRef := c.getBlobRef()
	if err := blobRef.GetMetadata(&storage.GetBlobMetadataOptions{LeaseID: c.leaseID}); err != nil {
		return fmt.Errorf("error getting metadata: %s", err)
	}
	if info == nil {
		delete(blobRef.Metadata, lockinfo)
	} else {
		blobRef.Metadata[lockinfo] = base64.StdEncoding.EncodeToString(info.Marshal())
	}
	return blobRef.SetMetadata(&storage.SetBlobMetadataOptions{LeaseID: c.leaseID})
}
