package remote

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/hashicorp/terraform/addrs"
	"github.com/hashicorp/terraform/backend/remote-state/secureazurerm/properties"
	"github.com/hashicorp/terraform/backend/remote-state/secureazurerm/remote/account/blob"
	"github.com/hashicorp/terraform/backend/remote-state/secureazurerm/remote/keyvault"
	"github.com/hashicorp/terraform/state"
	"github.com/hashicorp/terraform/states"
	"github.com/hashicorp/terraform/states/statefile"
	ctyjson "github.com/zclconf/go-cty/cty/json"
)

// State contains the remote state.
type State struct {
	mu sync.Mutex

	Blob     *blob.Blob         // client to communicate with the state blob storage.
	KeyVault *keyvault.KeyVault // client to communicate with the state key vault.

	Props *properties.Properties

	lineage      string
	serial       uint64
	disableLocks bool

	state, // current in-memory state.
	readState *states.State // state read from the blob

	secretIDs map[string]keyvault.SecretMetadata
}

// outputState contains the state of each output variable.
type outputState struct {
	ValueRaw     json.RawMessage `json:"value"`
	ValueTypeRaw json.RawMessage `json:"type"`
	Sensitive    bool            `json:"sensitive,omitempty"`
}

// resourceState contains the state of each resource.
type resourceState struct {
	Module         string                `json:"module,omitempty"`
	Mode           string                `json:"mode"`
	Type           string                `json:"type"`
	Name           string                `json:"name"`
	EachMode       string                `json:"each,omitempty"`
	ProviderConfig string                `json:"provider"`
	Instances      []instanceObjectState `json:"instances"`
}

// instanceObjectState contains the state of each instance of a resource.
type instanceObjectState struct {
	IndexKey interface{} `json:"index_key,omitempty"`
	Status   string      `json:"status,omitempty"`
	Deposed  string      `json:"deposed,omitempty"`

	SchemaVersion uint64          `json:"schema_version"`
	AttributesRaw json.RawMessage `json:"attributes,omitempty"`

	PrivateRaw []byte `json:"private,omitempty"`

	Dependencies []string `json:"depends_on,omitempty"`
}

// secureState contains the current state of infrastructure managed by Terraform.
type secureState struct {
	Version          string                 `json:"version"`
	TerraformVersion string                 `json:"terraform_version"`
	Serial           uint64                 `json:"serial"`
	Lineage          string                 `json:"lineage"`
	RootOutputs      map[string]outputState `json:"outputs"`
	Resources        []resourceState        `json:"resources"`
}

// State reads the state from the memory.
func (s *State) State() *states.State {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.state.DeepCopy()
}

// WriteState writes the new state to memory.
func (s *State) WriteState(ts *states.State) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Write the state to memory.
	s.state = ts.DeepCopy()
	return nil
}

// RefreshState fetches the state from the blob.
func (s *State) RefreshState() error {
	// Lock, milady.
	s.mu.Lock()
	defer s.mu.Unlock()

	// Get state data from the blob.
	payload, err := s.Blob.Get()
	if err != nil {
		return fmt.Errorf("error getting state from the blob: %s", err)
	}
	// Check if there is no data in the blob.
	if payload == nil {
		// Sync in-memory state with the empty blob.
		s.state = nil
		s.readState = nil
		s.lineage = ""
		s.serial = 0
		// Indicate that the blob contains no state.
		return nil
	}

	// Unmask remote state.
	var state secureState
	if err = json.Unmarshal(payload.Data, &state); err != nil {
		return fmt.Errorf("error unmarshalling state: %s", err)
	}
	if err = s.unmask(state.Resources); err != nil {
		return fmt.Errorf("error unmasking state: %s")
	}

	// Convert it back to terraform.State.
	j, err := json.Marshal(stateMap)
	if err != nil {
		return fmt.Errorf("error marshalling map to JSON: %s", err)
	}
	var state states.State
	if err := json.Unmarshal(j, &state); err != nil {
		return fmt.Errorf("error unmarshalling JSON to terraform.State: %s", err)
	}
	s.state = &state

	// Make a copy used to track changes.
	s.readState = s.state.DeepCopy()
	return nil
}

func appendInstanceObjectState(rs *states.Resource, is *states.ResourceInstance, key addrs.InstanceKey, instance *states.ResourceInstanceObjectSrc, deposed states.DeposedKey, instanceState []instanceObjectState) ([]instanceObjectState, error) {
	var status string
	switch instance.Status {
	case states.ObjectReady:
		status = ""
	case states.ObjectTainted:
		status = "tainted"
	default:
		return nil, fmt.Errorf("instance %s has status %s, which cannot be saved in state", rs.Addr.Instance(key), instance.Status)
	}

	var privateRaw []byte
	if len(instance.Private) > 0 {
		privateRaw = instance.Private
	}

	deps := make([]string, len(instance.Dependencies))
	for i, depAddr := range instance.Dependencies {
		deps[i] = depAddr.String()
	}

	var rawKey interface{}
	switch tk := key.(type) {
	case addrs.IntKey:
		rawKey = int(tk)
	case addrs.StringKey:
		rawKey = string(tk)
	default:
		if key != addrs.NoKey {
			return nil, fmt.Errorf("instance %s has an unsupported instance key: %#v", rs.Addr.Instance(key), key)
		}
	}

	j := instance.AttrsJSON

	return append(instanceState, instanceObjectState{
		IndexKey:      rawKey,
		Deposed:       string(deposed),
		Status:        status,
		SchemaVersion: instance.SchemaVersion,
		AttributesRaw: j,
		PrivateRaw:    privateRaw,
		Dependencies:  deps,
	}), nil
}

// PersistState saves the in-memory state to the blob.
func (s *State) PersistState() error {
	// Lock, harr!
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.state == nil {
		return errors.New("state is empty")
	}

	// Get state key vault's access policies.
	accessPolicies, err := s.KeyVault.GetAccessPolicies(context.Background())
	if err != nil {
		return fmt.Errorf("error getting the state key vault's access policies: %s", err)
	}
	for i, policy := range accessPolicies {
		// Remove itself from the access policy list.
		if *policy.ObjectID == s.Props.ObjectID {
			accessPolicies = append(accessPolicies[:i], accessPolicies[i+1:]...)
			break
		}
	}

	// List and save all secrets from the keyvault.
	s.secretIDs, err = s.KeyVault.ListSecrets(context.Background())
	if err != nil {
		return fmt.Errorf("error listing secrets: %s", err)
	}

	// Check for any changes to the in-memory state.
	if s.readState != nil {
		if !statefile.StatesMarshalEqual(s.state, s.readState) {
			s.serial++
		}
	}

	file := statefile.New(s.state, s.lineage, s.serial)
	state := &secureState{
		TerraformVersion: file.TerraformVersion.String(),
		Serial:           file.Serial,
		Lineage:          file.Lineage,
		RootOutputs:      map[string]outputState{},
		Resources:        []resourceState{},
	}

	for name, os := range file.State.RootModule().OutputValues {
		src, err := ctyjson.Marshal(os.Value, os.Value.Type())
		if err != nil {
			return fmt.Errorf("error serializing output value %q: %s", name, err)
		}

		typeSrc, err := ctyjson.MarshalType(os.Value.Type())
		if err != nil {
			return fmt.Errorf("error serializing the type of output value %q: %s", name, err)
		}

		state.RootOutputs[name] = outputState{
			Sensitive:    os.Sensitive,
			ValueRaw:     json.RawMessage(src),
			ValueTypeRaw: json.RawMessage(typeSrc),
		}
	}

	for _, module := range file.State.Modules {
		moduleAddr := module.Addr
		for _, resource := range module.Resources {
			// Get resource address.
			resourceAddr := resource.Addr

			// Set mode.
			var mode string
			switch resourceAddr.Mode {
			case addrs.ManagedResourceMode:
				mode = "managed"
			case addrs.DataResourceMode:
				mode = "data"
			default:
				return fmt.Errorf("resource %s has mode %s, which cannot be serialized in state", resourceAddr.Absolute(moduleAddr), resourceAddr.Mode)
			}

			// Set "each"-mode.
			var eachMode string
			switch resource.EachMode {
			case states.NoEach:
				eachMode = ""
			case states.EachList:
				eachMode = "list"
			case states.EachMap:
				eachMode = "map"
			default:
				return fmt.Errorf("resource %s has \"each\" mode %s, which cannot be serialized in state", resourceAddr.Absolute(moduleAddr), resource.EachMode)
			}

			// Append resource to the state-file.
			state.Resources = append(state.Resources, resourceState{
				Module:         moduleAddr.String(),
				Mode:           mode,
				Type:           resourceAddr.Type,
				Name:           resourceAddr.Name,
				EachMode:       eachMode,
				ProviderConfig: resource.ProviderConfig.String(),
				Instances:      []instanceObjectState{},
			})

			// Append instances to the state of resource.
			resourceState := &state.Resources[len(state.Resources)-1]
			for key, instance := range resource.Instances {
				if instance.HasCurrent() {
					if resourceState.Instances, err = appendInstanceObjectState(resource, instance, key, instance.Current, states.NotDeposed, resourceState.Instances); err != nil {
						return fmt.Errorf("error appending instance object: %s", err)
					}
				}
				for deposedKey, deposedObject := range instance.Deposed {
					if resourceState.Instances, err = appendInstanceObjectState(resource, instance, key, deposedObject, deposedKey, resourceState.Instances); err != nil {
						return fmt.Errorf("error appending deposed instance object: %s", err)
					}
				}
			}

			// Mask the resource state.
			if err := s.mask(state.Resources); err != nil {
				return fmt.Errorf("error masking module: %s", err)
			}
		}
		// Compare the existing access policies with current resources. Delete those that does not exist anymore.
		for _, accessPolicy := range accessPolicies {
			for _, resource := range state.Resources {
				for _, instance := range resource.Instances {
					var attributes map[string]interface{}
					if err := json.Unmarshal(instance.AttributesRaw, &attributes); err != nil {
						return fmt.Errorf("error unmarshalling attributes: %s", err)
					}
					identities, ok := attributes["identity"].(map[string]interface{})
					if !ok {
						continue
					}
					for _, identity := range identities {
						id := identity.(map[string]interface{})
						if *accessPolicy.ObjectID == id["principal_id"].(string) {
							goto end
						}
					}
				}
			}
			if err = s.KeyVault.RemoveIDFromAccessPolicies(context.Background(), *accessPolicy.TenantID, *accessPolicy.ObjectID); err != nil {
				return fmt.Errorf("error removing managed ID from access policies: %s", err)
			}
		end:
		}

		/*
			// Give resources access to the state as described in access_policies in the configuration.
			for _, accessPolicy := range s.Props.AccessPolicies {
				resource, ok := mod["resources"].(map[string]interface{})[strings.Join(accessPolicyDotSplitted[len(path):], ".")]
				if !ok {
					continue // could not find resource, perhaps due to being destroyed.
				}
				attributes := resource.(map[string]interface{})["primary"].(map[string]interface{})["attributes"].(map[string]interface{})
				value, ok := attributes["identity.#"]
				if !ok {
					return fmt.Errorf("access_policies contains a resource with no managed identity: %s", err)
				}
				length, err := strconv.Atoi(value.(string))
				if err != nil {
					return fmt.Errorf("error converting identity.# to integer: %s", err)
				}
				for i := 0; i < length; i++ {
					managedIdentity := keyvault.ManagedIdentity{
						PrincipalID: attributes[fmt.Sprintf("identity.%d.principal_id", i)].(string),
						TenantID:    attributes[fmt.Sprintf("identity.%d.tenant_id", i)].(string),
					}
					s.KeyVault.AddIDToAccessPolicies(context.Background(), &managedIdentity)
				}
			}
		*/
	}

	// Delete the resource's attributes that does not exists anymore in the key vault.
	resourceAttributeSecretIDs := make(map[string]struct{})
	for _, resource := range state.Resources {
		for _, instance := range resource.Instances {
			var attributes map[string]interface{}
			if err := json.Unmarshal(instance.AttributesRaw, &attributes); err != nil {
				return fmt.Errorf("error unmarshalling attributes: %s", err)
			}
			for _, attribute := range attributes {
				if object, ok := attribute.(secretAttribute); ok {
					resourceAttributeSecretIDs[object.ID] = struct{}{}
				}
			}
		}
	}
	for secretID := range s.secretIDs {
		if _, ok := resourceAttributeSecretIDs[secretID]; !ok {
			if err := s.KeyVault.DeleteSecret(context.Background(), secretID); err != nil {
				return fmt.Errorf("error deleting secret %s: %s", secretID, err)
			}
			delete(s.secretIDs, secretID)
		}
	}

	// Marshal state map to JSON.
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("error marshalling map: %s", err)
	}
	data = append(data, '\n')
	// Put it into the blob.
	if err := s.Blob.Put(data); err != nil {
		return fmt.Errorf("error leasing and putting buffer: %s", err)
	}

	// Set the persisted state as our new main reference state.
	s.readState = s.state.DeepCopy()

	return nil
}

// Lock locks the state.
func (s *State) Lock(info *state.LockInfo) (string, error) {
	if s.disableLocks {
		return "", nil
	}
	return s.Blob.Lock(info)
}

// Unlock unlocks the state.
func (s *State) Unlock(id string) error {
	if s.disableLocks {
		return nil
	}
	return s.Blob.Unlock(id)
}

// DisableLocks turns the Lock and Unlock methods into no-ops.
func (s *State) DisableLocks() {
	s.disableLocks = true
}
