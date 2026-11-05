package trie

import (
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/rlp"
)

// UpdateStorageByTrieKey associates trieKey with value in the storage trie without hashing trieKey:
// trieKey is already the trie key of a slot, keccak(slot), for a slot whose raw key is not known. The
// value is stored RLP-encoded, exactly as UpdateStorage stores it, so a slot written here and one
// written through UpdateStorage at the same trie key are indistinguishable. A zero-length value
// deletes the entry, as in UpdateStorage.
//
// No preimage is recorded: the raw key is what the caller does not have. trieKey must be 32 bytes,
// the length every key of a secure storage trie has; anything else is refused rather than written
// at a path no hashed key can reach.
//
// RNK fork addition, used by core/state's SetStorageByTrieKey. Pinned by
// TestStateTrieUpdateStorageByTrieKeyMatchesUpdateStorage.
func (t *StateTrie) UpdateStorageByTrieKey(_ common.Address, trieKey, value []byte) error {
	if len(trieKey) != common.HashLength {
		return fmt.Errorf("storage trie key is %d bytes, want %d", len(trieKey), common.HashLength)
	}
	if len(value) == 0 {
		return t.trie.Delete(trieKey)
	}
	v, err := rlp.EncodeToBytes(value)
	if err != nil {
		return err
	}
	return t.trie.Update(trieKey, v)
}

// DeleteStorageByTrieKey removes the entry at trieKey, without hashing it. Removing an absent entry
// changes nothing. trieKey must be 32 bytes, as in UpdateStorageByTrieKey.
//
// RNK fork addition, used by core/state's SetStorageByTrieKey.
func (t *StateTrie) DeleteStorageByTrieKey(_ common.Address, trieKey []byte) error {
	if len(trieKey) != common.HashLength {
		return fmt.Errorf("storage trie key is %d bytes, want %d", len(trieKey), common.HashLength)
	}
	return t.trie.Delete(trieKey)
}
