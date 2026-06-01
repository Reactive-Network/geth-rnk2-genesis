package state

var trieContractStorageHash = true

// For genesis initialization trieContractStorageHash = false
func SetTrieContractStorageHash(enabled bool) {
	trieContractStorageHash = enabled
}
