package comm

import (
	"encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/Azure/azure-sdk-for-go/storage"
	"github.com/hashicorp/terraform/state"
)

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
