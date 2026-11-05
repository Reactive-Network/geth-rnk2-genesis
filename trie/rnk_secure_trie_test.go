package trie

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
)

// TestStateTrieUpdateStorageByTrieKeyMatchesUpdateStorage holds a trie-key write to exactly the trie a
// raw-key write at the same slot builds: same root after an update, same root after a deletion, and a
// key of any length other than 32 bytes refused.
func TestStateTrieUpdateStorageByTrieKeyMatchesUpdateStorage(t *testing.T) {
	db := newTestDatabase(rawdb.NewMemoryDatabase(), rawdb.HashScheme)
	open := func() *StateTrie {
		tr, err := NewStateTrie(TrieID(types.EmptyRootHash), db)
		if err != nil {
			t.Fatal(err)
		}
		return tr
	}
	var (
		addr  = common.HexToAddress("0x01")
		slots = []common.Hash{common.HexToHash("0x01"), common.HexToHash("0x02"), common.HexToHash("0x03")}
		value = []byte{0xca, 0xfe}
	)
	byRaw, byTrieKey := open(), open()
	for _, slot := range slots {
		if err := byRaw.UpdateStorage(addr, slot[:], value); err != nil {
			t.Fatal(err)
		}
		if err := byTrieKey.UpdateStorageByTrieKey(addr, crypto.Keccak256(slot[:]), value); err != nil {
			t.Fatal(err)
		}
	}
	if byRaw.Hash() != byTrieKey.Hash() {
		t.Fatalf("after updates: raw-key root %x, trie-key root %x", byRaw.Hash(), byTrieKey.Hash())
	}
	if err := byRaw.DeleteStorage(addr, slots[1][:]); err != nil {
		t.Fatal(err)
	}
	if err := byTrieKey.DeleteStorageByTrieKey(addr, crypto.Keccak256(slots[1][:])); err != nil {
		t.Fatal(err)
	}
	if err := byTrieKey.UpdateStorageByTrieKey(addr, crypto.Keccak256(slots[2][:]), nil); err != nil {
		t.Fatal(err)
	}
	if err := byRaw.DeleteStorage(addr, slots[2][:]); err != nil {
		t.Fatal(err)
	}
	if byRaw.Hash() != byTrieKey.Hash() {
		t.Fatalf("after deletions: raw-key root %x, trie-key root %x", byRaw.Hash(), byTrieKey.Hash())
	}
	if err := byTrieKey.UpdateStorageByTrieKey(addr, []byte{1, 2, 3}, value); err == nil {
		t.Fatal("a 3-byte trie key was written")
	}
	if err := byTrieKey.DeleteStorageByTrieKey(addr, []byte{1, 2, 3}); err == nil {
		t.Fatal("a 3-byte trie key was deleted")
	}
}
