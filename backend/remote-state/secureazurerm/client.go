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

// Client holds the state to communicate with Azure Resource Manager.
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
	// Check if client's field are set correctly.
	if err := c.isValid(); err != nil {
		return nil, err
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
		return err
	}

	// Get blob reference to the remote blob in the container in the storage account.
	blobRef := c.getBlobRef()
	// Set blob content type, which is JSON.
	blobRef.Properties.ContentType = "application/json"
	// Set blob content length.
	blobRef.Properties.ContentLength = int64(len(data))

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

		// Get blob metadata.
		if err = blobRef.GetMetadata(&storage.GetBlobMetadataOptions{LeaseID: c.leaseID}); err != nil {
			return err
		}
	}

	// Create a block blob and upload the remote state in JSON to the blob.
	if err = blobRef.CreateBlockBlobFromReader(bytes.NewReader(data), &storage.PutBlobOptions{LeaseID: c.leaseID}); err != nil {
		return err
	}
	return blobRef.SetProperties(&storage.SetBlobPropertiesOptions{LeaseID: c.leaseID})
}

// Delete deletes blob that contains the state.
func (c *Client) Delete() error {
	// Is client's fields set correctly?
	if err := c.isValid(); err != nil {
		return err
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
	// Check if client's fields are set correctly.
	if err := c.isValid(); err != nil {
		return "", err
	}

	blobRef := c.getBlobRef()
	var err error
	leaseID, err := blobRef.AcquireLease(-1, info.ID, &storage.LeaseOptions{})
	// If failed to acquire lease.
	if err != nil {
		return "", err
	}
	info.ID = leaseID
	c.leaseID = info.ID

	if err = c.writeLockInfo(info); err != nil {
		return "", err
	}

	info.Path = fmt.Sprintf("%s/%s", c.containerName, c.blobName)
	return info.ID, nil
}

// Unlock breaks the lease of the blob.
func (c *Client) Unlock(id string) error {
	// Check if client's fields are set correctly.
	if err := c.isValid(); err != nil {
		return err
	}

	lockErr := &state.LockError{}
	lockInfo, err := c.getLockInfo()
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
	c.leaseID = ""
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

func (c *Client) isLeased() error {
	// Check if no lease has been acquired.
	if c.leaseID == "" {
		return fmt.Errorf("no lease has been acquired on blob")
	}
	return nil
}

const (
	lockInfoMetaKey = "terraformlockid" // Must be lower case!
)

// getLockInfo retrieves lock info from metadata.
func (c *Client) getLockInfo() (*state.LockInfo, error) {
	blobRef := c.getBlobRef()

	if err := blobRef.GetMetadata(&storage.GetBlobMetadataOptions{}); err != nil {
		return nil, err
	}

	raw := blobRef.Metadata[lockInfoMetaKey]
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
	blobRef := c.getBlobRef()
	if err := blobRef.GetMetadata(&storage.GetBlobMetadataOptions{LeaseID: c.leaseID}); err != nil {
		return err
	}
	if info == nil {
		delete(blobRef.Metadata, lockInfoMetaKey)
	} else {
		blobRef.Metadata[lockInfoMetaKey] = base64.StdEncoding.EncodeToString(info.Marshal())
	}
	return blobRef.SetMetadata(&storage.SetBlobMetadataOptions{LeaseID: c.leaseID})
}
