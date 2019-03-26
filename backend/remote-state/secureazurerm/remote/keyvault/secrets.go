package keyvault

// getSecretVersion gets the secret version from ID.
func getSecretVersion(ID string) string {
	i := len(ID) - 1
	for ID[i] != '/' {
		i--
	}
	return ID[i+1 : len(ID)]
}

// Insert inserts a secret into the key vault. Returns the version.
func (k *KeyVault) Insert(secret string) string {
	return ""
}
