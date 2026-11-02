package state

import (
	"errors"
	"fmt"
	"maps"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/ethereum/go-ethereum/triedb/database"
)

// Storage writes addressed by trie key (RNK fork addition).
//
// A storage slot is normally addressed by its raw key: SetState(addr, slot, value) writes the storage
// trie at keccak(slot) and the flat state at keccak(slot). Where the raw key is unknown and only the trie
// key keccak(slot) is (the chain's migrated genesis, whose storage was copied out of a trie without
// preimages), SetStorageByTrieKey writes the same two places at the trie key directly, without hashing
// it again. A zero value deletes the slot from both.
//
// Semantics, each pinned by a test in rnk_trie_key_storage_test.go:
//
//   - The storage trie entry at trieKey and the flat state entry at trieKey both end the block at the
//     value last written, so the account's storage root and its flat storage stay consistent
//     (TestSetStorageByTrieKeyWritesTrieAndFlatState).
//   - The flat state is written only where the value differs from the flat value at the StateDB's
//     original root; a write that only repairs the trie leaves the flat state untouched
//     (TestATrieOnlyRepairWritesNoFlatState).
//   - Reads by raw key in the same StateDB see the write: GetState(addr, slot) returns the value
//     written at keccak(slot) (TestARawReadSeesATrieKeyWrite).
//   - Raw-key writes after it win, in the trie and in the flat state alike
//     (TestARawWriteAfterATrieKeyWriteWins).
//   - Ordering: every trie-key write to an account must precede every raw-key access to that
//     account's storage in this StateDB until the next Commit. A trie-key write after a raw-key read
//     or write is refused with an error rather than ordered (TestATrieKeyWriteAfterARawAccessIsRefused).
//   - The account must exist; a write to an absent account is refused.
//   - It is not journaled: RevertToSnapshot does not undo it. Call it outside transaction execution.
//   - State history: the write records no raw-key origin, because there is no raw key. Under
//     rawStorageKey history, which a pathdb node with noStorageWiping writes, the block carrying such a
//     write therefore cannot be reverted: Recover across it fails with "failed to reset storage trie"
//     (or "failed to clear storage trie" for an account created in that block) before writing
//     anything, and the state is left as it was (TestRecoverAcrossATrieKeyWriteFailsAndCorruptsNothing).
//     The hash-keyed origin is recorded, so hash-keyed history stays correct.
//   - Only the merkle path database is supported: the flat origin is read through the trie database's
//     StateReader, and a verkle or hash-scheme database refuses the write.

// trieKeyWriter is the storage trie's half of a trie-key write. *trie.StateTrie implements it.
type trieKeyWriter interface {
	UpdateStorageByTrieKey(addr common.Address, trieKey, value []byte) error
	DeleteStorageByTrieKey(addr common.Address, trieKey []byte) error
}

// trieKeyStorage is one account's trie-key writes since its last commit.
type trieKeyStorage struct {
	values      Storage                  // trie key -> value last written
	origins     Storage                  // trie key -> flat value at the StateDB's original root
	uncommitted map[common.Hash]struct{} // trie keys not yet applied to the storage trie
	reader      database.StateReader     // flat state at the StateDB's original root, opened on first use
}

func (t *trieKeyStorage) copy() *trieKeyStorage {
	if t == nil {
		return nil
	}
	return &trieKeyStorage{
		values:      maps.Clone(t.values),
		origins:     maps.Clone(t.origins),
		uncommitted: maps.Clone(t.uncommitted),
		reader:      t.reader,
	}
}

// SetStorageByTrieKey writes value at the storage trie key trieKey of addr, which is already
// keccak(slot), without hashing it again, into both the storage trie and the flat state. A zero value
// deletes. See the file comment for the full semantics and its preconditions.
func (s *StateDB) SetStorageByTrieKey(addr common.Address, trieKey, value common.Hash) error {
	obj := s.getStateObject(addr)
	if obj == nil {
		return fmt.Errorf("account %s does not exist; a storage write by trie key needs an existing account", addr.Hex())
	}
	if err := obj.setStorageByTrieKey(trieKey, value); err != nil {
		return err
	}
	// Mark the account for the next IntermediateRoot directly, not through the journal: the write is
	// not journaled, and Finalise's empty-account deletion must not treat it as a touch.
	s.markUpdate(addr)
	return nil
}

func (s *stateObject) setStorageByTrieKey(trieKey, value common.Hash) error {
	if s.db.db.TrieDB().IsVerkle() {
		return errors.New("storage writes by trie key are not supported on a verkle database")
	}
	if _, destructed := s.db.stateObjectsDestruct[s.address]; destructed {
		return fmt.Errorf("account %s was destructed in this block; its storage cannot be written by trie key", s.address.Hex())
	}
	if len(s.originStorage) > 0 || len(s.dirtyStorage) > 0 || len(s.pendingStorage) > 0 || len(s.uncommittedStorage) > 0 {
		return fmt.Errorf("account %s has storage accessed by raw key in this state; every trie-key write must precede every raw-key access until the next commit", s.address.Hex())
	}
	if s.trieKeyWrites == nil {
		s.trieKeyWrites = &trieKeyStorage{
			values:      make(Storage),
			origins:     make(Storage),
			uncommitted: make(map[common.Hash]struct{}),
		}
	}
	tk := s.trieKeyWrites
	if _, known := tk.origins[trieKey]; !known {
		origin, err := s.flatOriginByTrieKey(trieKey)
		if err != nil {
			return err
		}
		tk.origins[trieKey] = origin
	}
	tk.values[trieKey] = value
	tk.uncommitted[trieKey] = struct{}{}
	return nil
}

// flatOriginByTrieKey is the flat state's value at trieKey at the StateDB's original root, or zero for an
// account that did not exist there.
func (s *stateObject) flatOriginByTrieKey(trieKey common.Hash) (common.Hash, error) {
	if s.origin == nil {
		return common.Hash{}, nil
	}
	tk := s.trieKeyWrites
	if tk.reader == nil {
		reader, err := s.db.db.TrieDB().StateReader(s.db.originalRoot)
		if err != nil {
			return common.Hash{}, fmt.Errorf("flat state at %s is not readable, so a storage write by trie key cannot be checked against it: %w", s.db.originalRoot.Hex(), err)
		}
		tk.reader = reader
	}
	blob, err := tk.reader.Storage(s.addrHash(), trieKey)
	if err != nil {
		return common.Hash{}, fmt.Errorf("flat storage of %s at trie key %s: %w", s.address.Hex(), trieKey.Hex(), err)
	}
	if len(blob) == 0 {
		return common.Hash{}, nil
	}
	_, content, _, err := rlp.Split(blob)
	if err != nil {
		return common.Hash{}, fmt.Errorf("flat storage of %s at trie key %s does not decode: %w", s.address.Hex(), trieKey.Hex(), err)
	}
	if len(content) > common.HashLength {
		return common.Hash{}, fmt.Errorf("flat storage of %s at trie key %s is %d bytes, more than a slot holds", s.address.Hex(), trieKey.Hex(), len(content))
	}
	return common.BytesToHash(content), nil
}

// trieKeyValue serves a raw-key read from the trie-key writes: the value written at keccak(key), if any.
func (s *stateObject) trieKeyValue(key common.Hash) (common.Hash, bool) {
	if s.trieKeyWrites == nil || len(s.trieKeyWrites.values) == 0 {
		return common.Hash{}, false
	}
	value, ok := s.trieKeyWrites.values[crypto.Keccak256Hash(key[:])]
	return value, ok
}

// trieKeyWritesPending reports trie-key writes not yet applied to the storage trie.
func (s *stateObject) trieKeyWritesPending() bool {
	return s.trieKeyWrites != nil && len(s.trieKeyWrites.uncommitted) > 0
}

// applyTrieKeyWrites applies the trie-key writes not yet applied to tr, updates before deletions for
// the reason updateTrie gives. They are applied before the raw-key writes of the same pass, which
// follow them in time (the ordering precondition), so a raw-key write to the same slot wins.
func (s *stateObject) applyTrieKeyWrites(tr Trie) error {
	if !s.trieKeyWritesPending() {
		return nil
	}
	writer, ok := tr.(trieKeyWriter)
	if !ok {
		return fmt.Errorf("storage trie of %s (%T) cannot be written by trie key", s.address.Hex(), tr)
	}
	tk := s.trieKeyWrites
	var deletions []common.Hash
	for key := range tk.uncommitted {
		value := tk.values[key]
		if value == (common.Hash{}) {
			deletions = append(deletions, key)
			continue
		}
		if err := writer.UpdateStorageByTrieKey(s.address, key[:], common.TrimLeftZeroes(value[:])); err != nil {
			return err
		}
		s.db.StorageUpdated.Add(1)
	}
	for _, key := range deletions {
		if err := writer.DeleteStorageByTrieKey(s.address, key[:]); err != nil {
			return err
		}
		s.db.StorageDeleted.Add(1)
	}
	tk.uncommitted = make(map[common.Hash]struct{})
	return nil
}

// commitTrieKeyWrites fills the flat-state diff and the hash-keyed origins of op from the trie-key
// writes, and reports whether there were any, in which case the storage trie must be committed even if
// the flat diff is empty. It runs before the raw-key loop of commitStorage, which overwrites a flat
// entry it shares and leaves the origin recorded here in place.
func (s *stateObject) commitTrieKeyWrites(op *accountUpdate, encode func(common.Hash) []byte) bool {
	if s.trieKeyWrites == nil || len(s.trieKeyWrites.values) == 0 {
		s.trieKeyWrites = nil
		return false
	}
	tk := s.trieKeyWrites
	for key, value := range tk.values {
		origin := tk.origins[key]
		if value == origin {
			continue
		}
		if op.storages == nil {
			op.storages = make(map[common.Hash][]byte)
		}
		op.storages[key] = encode(value)
		if op.storagesOriginByHash == nil {
			op.storagesOriginByHash = make(map[common.Hash][]byte)
		}
		op.storagesOriginByHash[key] = encode(origin)
	}
	s.trieKeyWrites = nil
	return true
}
