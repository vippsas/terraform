package secureazurerm

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"

	//"github.com/Azure/azure-sdk-for-go/services/keyvault/v7.0/keyvault"
	"github.com/Azure/azure-sdk-for-go/storage"
	"github.com/hashicorp/go-multierror"
	"github.com/hashicorp/go-uuid"
	"github.com/hashicorp/terraform/state"
	"github.com/hashicorp/terraform/state/remote"
	"github.com/hashicorp/terraform/terraform"
)

const (
	lockInfoMetaKey = "terraformlockid" // Must be lower case!
)

// Client holds the state to communicate with Azure Resource Manager.
type Client struct {
	// Client to operate on Azure Storage Account:
	blobClient    storage.BlobStorageClient // Client to communicate with Azure Resource Manager to operate on Azure Storage Accounts.
	containerName string                    // The name of the container that contains the blob storing the remote state in JSON.
	blobName      string                    // The name of the blob that stores the remote state in JSON.
	leaseID       string                    // ??

	// Client to operate on Azure Key Vault:
	//keyVaultClient keyvault.BaseClient // Client to communicate with Azure Resource Manager to operate on Azure Key Vaults.
	keyVaultName string // The name of the key vault that stores the sensitive values of the remote state.
}

// Get gets the remote state from Azure Storage Account.
func (c *Client) Get() (*remote.Payload, error) {
	// Get blob containing remote state.
	containerReference := c.blobClient.GetContainerReference(c.containerName)
	blobReference := containerReference.GetBlobReference(c.blobName)
	options := &storage.GetBlobOptions{}
	if c.leaseID != "" {
		options.LeaseID = c.leaseID
	}
	blob, err := blobReference.Get(options)
	if err != nil {
		if storErr, ok := err.(storage.AzureStorageServiceError); ok {
			if storErr.Code == "BlobNotFound" {
				return nil, nil
			}
		}
		return nil, err
	}

	// Get remote state from blob.
	defer blob.Close() // TODO: Handle close error.
	buf := bytes.NewBuffer(nil)
	if _, err := io.Copy(buf, blob); err != nil {
		return nil, fmt.Errorf("failed to read remote state: %s", err)
	}
	payload := &remote.Payload{
		Data: buf.Bytes(), // remote state data.
	}
	if len(payload.Data) == 0 { // blob is empty.
		return nil, nil
	}
	return payload, nil
}

// Put puts the remote state data into a blob and the key vault.
func (c *Client) Put(data []byte) error {
	getOptions := &storage.GetBlobMetadataOptions{}
	setOptions := &storage.SetBlobPropertiesOptions{}
	putOptions := &storage.PutBlobOptions{}

	containerReference := c.blobClient.GetContainerReference(c.containerName)
	blobReference := containerReference.GetBlobReference(c.blobName)

	// Set blob content type, which is JSON.
	blobReference.Properties.ContentType = "application/json"
	// Set blob content length.
	blobReference.Properties.ContentLength = int64(len(data))

	if c.leaseID != "" {
		getOptions.LeaseID = c.leaseID
		setOptions.LeaseID = c.leaseID
		putOptions.LeaseID = c.leaseID
	}

	// Check if blob exists.
	exists, err := blobReference.Exists()
	if err != nil {
		return err
	}
	if exists {
		if err = blobReference.GetMetadata(getOptions); err != nil {
			return err
		}
	}

	// Create a blob and upload the remote state in JSON to the blob.
	if err = blobReference.CreateBlockBlobFromReader(bytes.NewReader(data), putOptions); err != nil {
		return err
	}
	return blobReference.SetProperties(setOptions)
}

// Delete deletes blob that contains the blob state.
func (c *Client) Delete() error {
	// Get container from storage account.
	containerReference := c.blobClient.GetContainerReference(c.containerName)

	// Get blob from blob container.
	blobReference := containerReference.GetBlobReference(c.blobName)

	// Delete blob.
	options := &storage.DeleteBlobOptions{} // Set delete blob options.
	if c.leaseID != "" {
		options.LeaseID = c.leaseID
	}
	return blobReference.Delete(options) // Call the API to delete it!
}

// Lock locks the mutex of the storage account containing the remote state blob.
func (c *Client) Lock(info *state.LockInfo) (string, error) {
	info.Path = fmt.Sprintf("%s/%s", c.containerName, c.blobName)

	if info.ID == "" {
		lockID, err := uuid.GenerateUUID()
		if err != nil {
			return "", err
		}
		info.ID = lockID
	}

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

	containerReference := c.blobClient.GetContainerReference(c.containerName)
	blobReference := containerReference.GetBlobReference(c.blobName)
	leaseID, err := blobReference.AcquireLease(-1, info.ID, &storage.LeaseOptions{})
	if err != nil {
		if storErr, ok := err.(storage.AzureStorageServiceError); ok && storErr.Code != "BlobNotFound" {
			return "", getLockInfoErr(err)
		}

		// failed to lock as there was no state blob, write empty state
		remoteState := &remote.State{Client: c}

		// ensure state is actually empty
		if err := remoteState.RefreshState(); err != nil {
			return "", fmt.Errorf("failed to refresh state before writing empty state for locking: %s", err)
		}

		log.Print("[DEBUG] Could not lock as state blob did not exist, creating with empty state")

		if v := remoteState.State(); v == nil {
			if err := remoteState.WriteState(terraform.NewState()); err != nil {
				return "", fmt.Errorf("failed to write empty state for locking: %s", err)
			}
			if err := remoteState.PersistState(); err != nil {
				return "", fmt.Errorf("failed to persist empty state for locking: %s", err)
			}
		}

		leaseID, err = blobReference.AcquireLease(-1, info.ID, &storage.LeaseOptions{})
		if err != nil {
			return "", getLockInfoErr(err)
		}
	}

	info.ID = leaseID
	c.leaseID = leaseID

	if err := c.writeLockInfo(info); err != nil {
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

	containerReference := c.blobClient.GetContainerReference(c.containerName)
	blobReference := containerReference.GetBlobReference(c.blobName)
	err = blobReference.ReleaseLease(id, &storage.LeaseOptions{})
	if err != nil {
		lockErr.Err = err
		return lockErr
	}

	c.leaseID = ""

	return nil
}

// getLockInfo returns metadata about the lock.
func (c *Client) getLockInfo() (*state.LockInfo, error) {
	containerReference := c.blobClient.GetContainerReference(c.containerName)
	blobReference := containerReference.GetBlobReference(c.blobName)
	err := blobReference.GetMetadata(&storage.GetBlobMetadataOptions{})
	if err != nil {
		return nil, err
	}

	raw := blobReference.Metadata[lockInfoMetaKey]
	if raw == "" {
		return nil, fmt.Errorf("blob metadata %q was empty", lockInfoMetaKey)
	}

	data, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return nil, err
	}

	lockInfo := &state.LockInfo{}
	err = json.Unmarshal(data, lockInfo)
	if err != nil {
		return nil, err
	}

	return lockInfo, nil
}

// writeLockInfo writes info to blob metadata, and deletes metadata entry if info is nil.
func (c *Client) writeLockInfo(info *state.LockInfo) error {
	containerReference := c.blobClient.GetContainerReference(c.containerName)
	blobReference := containerReference.GetBlobReference(c.blobName)
	if err := blobReference.GetMetadata(&storage.GetBlobMetadataOptions{LeaseID: c.leaseID}); err != nil {
		return err
	}

	if info == nil {
		delete(blobReference.Metadata, lockInfoMetaKey)
	} else {
		value := base64.StdEncoding.EncodeToString(info.Marshal())
		blobReference.Metadata[lockInfoMetaKey] = value
	}

	opts := &storage.SetBlobMetadataOptions{
		LeaseID: c.leaseID,
	}
	return blobReference.SetMetadata(opts)
}
