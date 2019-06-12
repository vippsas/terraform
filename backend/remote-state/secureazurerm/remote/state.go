package remote

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/hashicorp/terraform/addrs"
	"github.com/hashicorp/terraform/backend/remote-state/secureazurerm/common"
	"github.com/hashicorp/terraform/backend/remote-state/secureazurerm/properties"
	"github.com/hashicorp/terraform/backend/remote-state/secureazurerm/remote/account/blob"
	"github.com/hashicorp/terraform/backend/remote-state/secureazurerm/remote/keyvault"
	"github.com/hashicorp/terraform/state"
	"github.com/hashicorp/terraform/states"
	"github.com/hashicorp/terraform/states/statefile"
	"github.com/hashicorp/terraform/tfdiags"
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

// State reads the state from the memory.
func (s *State) State() *states.State {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.state.DeepCopy()
}

// WriteState writes the state to memory.
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
	var secureState common.SecureState
	if err = json.Unmarshal(payload.Data, &secureState); err != nil {
		return fmt.Errorf("error unmarshalling state: %s", err)
	}
	if err = s.unmask(secureState.Resources); err != nil {
		return fmt.Errorf("error unmasking state: %s", err)
	}

	state := states.NewState()
	for _, resourceState := range secureState.Resources {
		resourceAddr := addrs.Resource{
			Type: resourceState.Type,
			Name: resourceState.Name,
		}
		switch resourceState.Mode {
		case "managed":
			resourceAddr.Mode = addrs.ManagedResourceMode
		case "data":
			resourceAddr.Mode = addrs.DataResourceMode
		default:
			return fmt.Errorf("state contains a resource with mode %q (%q %q) which is not supported", resourceState.Mode, resourceAddr.Type, resourceAddr.Name)
		}

		moduleAddr := addrs.RootModuleInstance
		if resourceState.Module != "" {
			var diags tfdiags.Diagnostics
			moduleAddr, diags = addrs.ParseModuleInstanceStr(resourceState.Module)
			if diags.HasErrors() {
				return fmt.Errorf("error parsing module: %s", err)
			}
		}

		providerAddr, addrDiags := addrs.ParseAbsProviderConfigStr(resourceState.ProviderConfig)
		if addrDiags.HasErrors() {
			return fmt.Errorf("error parsing provider: %s", err)
		}

		var eachMode states.EachMode
		switch resourceState.EachMode {
		case "":
			eachMode = states.NoEach
		case "list":
			eachMode = states.EachList
		case "map":
			eachMode = states.EachMap
		default:
			return fmt.Errorf("resource %s has invalid \"each\" value %q in state", resourceAddr.Absolute(moduleAddr), eachMode)
		}

		module := state.EnsureModule(moduleAddr)

		// Ensure the resource container object is present in the state.
		module.SetResourceMeta(resourceAddr, eachMode, providerAddr)

		for _, instanceState := range resourceState.Instances {
			keyRaw := instanceState.IndexKey
			var key addrs.InstanceKey
			switch tk := keyRaw.(type) {
			case int:
				key = addrs.IntKey(tk)
			case float64:
				// Since JSON only has one number type, reading from encoding/json
				// gives us a float64 here even if the number is whole.
				// float64 has a smaller integer range than int, but in practice
				// we rarely have more than a few tens of instances and so
				// it's unlikely that we'll exhaust the 52 bits in a float64.
				key = addrs.IntKey(int(tk))
			case string:
				key = addrs.StringKey(tk)
			default:
				if keyRaw != nil {
					return fmt.Errorf("resource %s has an instance with the invalid instance key %#v", resourceAddr.Absolute(moduleAddr), keyRaw)
				}
				key = addrs.NoKey
			}

			instanceAddress := resourceAddr.Instance(key)

			obj := &states.ResourceInstanceObjectSrc{
				SchemaVersion: instanceState.SchemaVersion,
			}

			// Instance attributes
			switch {
			case instanceState.AttributesRaw != nil:
				obj.AttrsJSON = instanceState.AttributesRaw
			default:
				return fmt.Errorf("empty attributes: %s", err)
			}

			// Status
			raw := instanceState.Status
			switch raw {
			case "":
				obj.Status = states.ObjectReady
			case "tainted":
				obj.Status = states.ObjectTainted
			default:
				return fmt.Errorf("instance %s has invalid status %q", instanceAddress.Absolute(moduleAddr), raw)
			}
			if raw := instanceState.PrivateRaw; len(raw) > 0 {
				obj.Private = raw
			}

			depsRaw := instanceState.Dependencies
			deps := make([]addrs.Referenceable, 0, len(depsRaw))
			for _, depRaw := range depsRaw {
				ref, refDiags := addrs.ParseRefStr(depRaw)
				if refDiags.HasErrors() {
					return fmt.Errorf("error parsing refStr: %s", err)
				}
				if len(ref.Remaining) != 0 {
					return fmt.Errorf("instance %s declares dependency on %q, which is not a reference to a dependable object", instanceAddress.Absolute(moduleAddr), depRaw)
				}
				if ref.Subject == nil {
					return fmt.Errorf("parsing dependency %q for instance %s returned a nil address", depRaw, instanceAddress.Absolute(moduleAddr)) // should never happen.
				}
				deps = append(deps, ref.Subject)
			}
			obj.Dependencies = deps

			if instanceState.Deposed != "" {
				dk := states.DeposedKey(instanceState.Deposed)
				if len(dk) != 8 {
					return fmt.Errorf("instance %s has an object with deposed key %q, which is not correctly formatted", instanceAddress.Absolute(moduleAddr), instanceState.Deposed)
				}
				is := module.ResourceInstance(instanceAddress)
				if is.HasDeposed(dk) {
					return fmt.Errorf("instance %s deposed object %q appears multiple times in the state file", instanceAddress.Absolute(moduleAddr), dk)
				}
				module.SetResourceInstanceDeposed(instanceAddress, dk, obj, providerAddr)
			} else {
				is := module.ResourceInstance(instanceAddress)
				if is.HasCurrent() {
					return fmt.Errorf("instance %s appears multiple times in the state file", instanceAddress.Absolute(moduleAddr))
				}
				module.SetResourceInstanceCurrent(instanceAddress, obj, providerAddr)
			}
		}

		module.SetResourceMeta(resourceAddr, eachMode, providerAddr)
	}

	rootModule := state.RootModule()
	for name, output := range secureState.RootOutputs {
		os := &states.OutputValue{}
		os.Sensitive = output.Sensitive

		ty, err := ctyjson.UnmarshalType([]byte(output.ValueTypeRaw))
		if err != nil {
			return fmt.Errorf("state has an invalid type specification for output %q: %s", name, err)
		}

		val, err := ctyjson.Unmarshal([]byte(output.ValueRaw), ty)
		if err != nil {
			return fmt.Errorf("state has an invalid value for output %q: %s", name, err)
		}

		os.Value = val
		rootModule.OutputValues[name] = os
	}

	s.state = state

	// Make a copy used to track changes.
	s.readState = s.state.DeepCopy()
	return nil
}

func appendInstanceObjectState(rs *states.Resource, is *states.ResourceInstance, key addrs.InstanceKey, instance *states.ResourceInstanceObjectSrc, deposed states.DeposedKey, instanceState []common.InstanceObjectState) ([]common.InstanceObjectState, error) {
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

	return append(instanceState, common.InstanceObjectState{
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
	state := &common.SecureState{
		TerraformVersion: file.TerraformVersion.String(),
		Serial:           file.Serial,
		Lineage:          file.Lineage,
		RootOutputs:      map[string]common.OutputState{},
		Resources:        []common.ResourceState{},
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

		state.RootOutputs[name] = common.OutputState{
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
			state.Resources = append(state.Resources, common.ResourceState{
				Module:         moduleAddr.String(),
				Mode:           mode,
				Type:           resourceAddr.Type,
				Name:           resourceAddr.Name,
				EachMode:       eachMode,
				ProviderConfig: resource.ProviderConfig.String(),
				Instances:      []common.InstanceObjectState{},
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
	b, err := json.MarshalIndent(&state, "", "  ")
	if err != nil {
		return fmt.Errorf("error marshalling map: %s", err)
	}
	b = append(b, '\n')

	// Put it into the blob.
	if err := s.Blob.Put(b); err != nil {
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
