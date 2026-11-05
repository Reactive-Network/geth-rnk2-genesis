package state

import (
	"bytes"
	"sort"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/ethdb/memorydb"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/ethereum/go-ethereum/trie"
	"github.com/ethereum/go-ethereum/triedb"
	"github.com/ethereum/go-ethereum/triedb/pathdb"
	"github.com/holiman/uint256"
)

// The tests for SetStorageByTrieKey (rnk_trie_key_storage.go). Every chain here is a path database with
// state history in a freezer, committed with noStorageWiping set, which is how the RNK node commits
// (raw-key state history). The genesis is committed the way the node's applyGenesis commits it: with
// SetTrieContractStorageHash(false) and allocation keys that are already keccak(slot), so the flat
// state holds keccak(slot) and the storage trie keccak(keccak(slot)).

var (
	tkAccount = common.HexToAddress("0x8888888888888888888888888888888888888888")
	tkOther   = common.HexToAddress("0x0000000000000000000000000000000000000001")
)

type tkChain struct {
	t     *testing.T
	disk  ethdb.Database
	tdb   *triedb.Database
	roots []common.Hash
}

func newTKChain(t *testing.T) *tkChain {
	t.Helper()
	disk, err := rawdb.Open(memorydb.New(), rawdb.OpenOptions{Ancient: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	c := &tkChain{t: t, disk: disk}
	c.tdb = c.open()
	t.Cleanup(func() { c.tdb.Close() })
	return c
}

func (c *tkChain) open() *triedb.Database {
	cfg := *pathdb.Defaults
	cfg.NoAsyncFlush = true
	cfg.NoAsyncGeneration = true
	return triedb.NewDatabase(c.disk, &triedb.Config{PathDB: &cfg})
}

func (c *tkChain) reopen() {
	c.t.Helper()
	if err := c.tdb.Close(); err != nil {
		c.t.Fatal(err)
	}
	c.tdb = c.open()
}

func (c *tkChain) head() common.Hash { return c.roots[len(c.roots)-1] }

func (c *tkChain) state() *StateDB {
	c.t.Helper()
	sdb, err := New(c.head(), NewDatabase(c.tdb, nil))
	if err != nil {
		c.t.Fatal(err)
	}
	return sdb
}

// commit commits sdb as the next block, raw-key history as the node writes it, and flattens it to disk
// as the node's persistState does after every block.
func (c *tkChain) commit(sdb *StateDB) common.Hash {
	c.t.Helper()
	root, err := sdb.Commit(uint64(len(c.roots)), true, true)
	if err != nil {
		c.t.Fatal(err)
	}
	if root == c.head() {
		// A block that changed nothing adds no layer.
		return root
	}
	if err := c.tdb.Commit(root, false); err != nil {
		c.t.Fatal(err)
	}
	c.roots = append(c.roots, root)
	return root
}

// genesis commits an allocation whose storage keys are already trie keys, as applyGenesis does.
func (c *tkChain) genesis(storage map[common.Hash]common.Hash) {
	c.t.Helper()
	sdb, err := New(types.EmptyRootHash, NewDatabase(c.tdb, nil))
	if err != nil {
		c.t.Fatal(err)
	}
	sdb.SetBalance(tkAccount, uint256.NewInt(1), tracing.BalanceChangeUnspecified)
	sdb.SetCode(tkAccount, []byte{0x00}, tracing.CodeChangeUnspecified)
	sdb.SetBalance(tkOther, uint256.NewInt(1), tracing.BalanceChangeUnspecified)
	for key, value := range storage {
		sdb.SetState(tkAccount, key, value)
	}
	SetTrieContractStorageHash(false)
	root, err := sdb.Commit(0, false, false)
	SetTrieContractStorageHash(true)
	if err != nil {
		c.t.Fatal(err)
	}
	if err := c.tdb.Commit(root, false); err != nil {
		c.t.Fatal(err)
	}
	c.roots = append(c.roots, root)
}

// trieEntries are the leaves of addr's storage trie at root, by trie key, RLP-encoded.
func (c *tkChain) trieEntries(root common.Hash, addr common.Address) map[common.Hash][]byte {
	c.t.Helper()
	sdb, err := New(root, NewDatabase(c.tdb, nil))
	if err != nil {
		c.t.Fatal(err)
	}
	out := map[common.Hash][]byte{}
	storageRoot := sdb.GetStorageRoot(addr)
	if storageRoot == types.EmptyRootHash || storageRoot == (common.Hash{}) {
		return out
	}
	tr, err := trie.NewStateTrie(trie.StorageTrieID(root, crypto.Keccak256Hash(addr[:]), storageRoot), c.tdb)
	if err != nil {
		c.t.Fatal(err)
	}
	it, err := tr.NodeIterator(nil)
	if err != nil {
		c.t.Fatal(err)
	}
	for it.Next(true) {
		if it.Leaf() {
			out[common.BytesToHash(it.LeafKey())] = common.CopyBytes(it.LeafBlob())
		}
	}
	if it.Error() != nil {
		c.t.Fatal(it.Error())
	}
	return out
}

// flatEntries are addr's flat storage entries at root, by trie key, RLP-encoded.
func (c *tkChain) flatEntries(root common.Hash, addr common.Address) map[common.Hash][]byte {
	c.t.Helper()
	it, err := c.tdb.StorageIterator(root, crypto.Keccak256Hash(addr[:]), common.Hash{})
	if err != nil {
		c.t.Fatal(err)
	}
	defer it.Release()
	out := map[common.Hash][]byte{}
	for it.Next() {
		out[it.Hash()] = common.CopyBytes(it.Slot())
	}
	if it.Error() != nil {
		c.t.Fatal(it.Error())
	}
	return out
}

// rootOf is the storage root of a trie holding exactly entries.
func rootOf(entries map[common.Hash][]byte) common.Hash {
	keys := make([]common.Hash, 0, len(entries))
	for key := range entries {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return bytes.Compare(keys[i][:], keys[j][:]) < 0 })
	st := trie.NewStackTrie(nil)
	for _, key := range keys {
		st.Update(key[:], entries[key])
	}
	return st.Hash()
}

func encodeSlot(v common.Hash) []byte {
	blob, _ := rlp.EncodeToBytes(common.TrimLeftZeroes(v[:]))
	return blob
}

// assertTrieEqualsFlat holds addr's storage trie at root to its flat storage: same entries, and the
// account's storage root is the root of a trie built from the flat entries.
func (c *tkChain) assertTrieEqualsFlat(root common.Hash, addr common.Address) {
	c.t.Helper()
	tr, flat := c.trieEntries(root, addr), c.flatEntries(root, addr)
	if len(tr) != len(flat) {
		c.t.Errorf("storage trie has %d entries, flat state %d", len(tr), len(flat))
	}
	for key, value := range flat {
		if !bytes.Equal(tr[key], value) {
			c.t.Errorf("trie key %x: trie %x, flat %x", key, tr[key], value)
		}
	}
	sdb, err := New(root, NewDatabase(c.tdb, nil))
	if err != nil {
		c.t.Fatal(err)
	}
	want := types.EmptyRootHash
	if len(flat) > 0 {
		want = rootOf(flat)
	}
	if got := sdb.GetStorageRoot(addr); got != want && !(got == (common.Hash{}) && want == types.EmptyRootHash) {
		c.t.Errorf("storage root %x, root of the flat entries %x", got, want)
	}
}

// misKeyedGenesis is two genesis slots, keyed as the migration wrote them.
func misKeyedGenesis() (slots []common.Hash, values []common.Hash, storage map[common.Hash]common.Hash) {
	slots = []common.Hash{common.HexToHash("0x00"), common.HexToHash("0x360894a13ba1a3210667c828492db98dca3e2076cc3735a920a3ca505d382bbc")}
	values = []common.Hash{common.HexToHash("0xaa"), common.HexToHash("0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")}
	storage = map[common.Hash]common.Hash{}
	for i := range slots {
		storage[crypto.Keccak256Hash(slots[i][:])] = values[i]
	}
	return
}

// repairAll makes the storage trie equal the flat state: every flat entry written at its own trie key,
// every trie-only entry deleted. It is what the RNK node's genesis storage rekey does, on one account.
func (c *tkChain) repairAll(sdb *StateDB, root common.Hash, addr common.Address) {
	c.t.Helper()
	flat, tr := c.flatEntries(root, addr), c.trieEntries(root, addr)
	for key, blob := range flat {
		_, content, _, err := rlp.Split(blob)
		if err != nil {
			c.t.Fatal(err)
		}
		if err := sdb.SetStorageByTrieKey(addr, key, common.BytesToHash(content)); err != nil {
			c.t.Fatal(err)
		}
	}
	for key := range tr {
		if _, ok := flat[key]; !ok {
			if err := sdb.SetStorageByTrieKey(addr, key, common.Hash{}); err != nil {
				c.t.Fatal(err)
			}
		}
	}
}

// TestSetStorageByTrieKeyWritesTrieAndFlatState: a write by trie key lands at that key in the storage
// trie and in the flat state, the account's storage root is the root of its flat entries, and a zero
// write deletes from both.
func TestSetStorageByTrieKeyWritesTrieAndFlatState(t *testing.T) {
	c := newTKChain(t)
	c.genesis(nil)
	key := crypto.Keccak256Hash([]byte("a slot whose raw key is unknown"))
	value := common.HexToHash("0x1234")

	sdb := c.state()
	if err := sdb.SetStorageByTrieKey(tkAccount, key, value); err != nil {
		t.Fatal(err)
	}
	root := c.commit(sdb)
	if got := c.flatEntries(root, tkAccount)[key]; !bytes.Equal(got, encodeSlot(value)) {
		t.Errorf("flat state at the trie key: %x, want %x", got, encodeSlot(value))
	}
	if got := c.trieEntries(root, tkAccount)[key]; !bytes.Equal(got, encodeSlot(value)) {
		t.Errorf("storage trie at the trie key: %x, want %x", got, encodeSlot(value))
	}
	c.assertTrieEqualsFlat(root, tkAccount)

	sdb = c.state()
	if err := sdb.SetStorageByTrieKey(tkAccount, key, common.Hash{}); err != nil {
		t.Fatal(err)
	}
	root = c.commit(sdb)
	if n := len(c.flatEntries(root, tkAccount)); n != 0 {
		t.Errorf("flat state holds %d entries after the zero write", n)
	}
	if n := len(c.trieEntries(root, tkAccount)); n != 0 {
		t.Errorf("storage trie holds %d entries after the zero write", n)
	}
	c.assertTrieEqualsFlat(root, tkAccount)
}

// TestATrieOnlyRepairWritesNoFlatState: on a mis-keyed genesis, writing each flat value at its own trie
// key and deleting the double-hashed entries repairs the trie, records no flat-state change, and leaves
// every raw-key read as it was.
func TestATrieOnlyRepairWritesNoFlatState(t *testing.T) {
	c := newTKChain(t)
	slots, values, storage := misKeyedGenesis()
	c.genesis(storage)
	before := c.state()
	for i := range slots {
		if got := before.GetState(tkAccount, slots[i]); got != values[i] {
			t.Fatalf("genesis read of slot %x: %x, want %x", slots[i], got, values[i])
		}
	}
	if tr, flat := c.trieEntries(c.head(), tkAccount), c.flatEntries(c.head(), tkAccount); rootOf(tr) == rootOf(flat) {
		t.Fatal("the fixture's trie already equals its flat state; it does not reproduce the mis-keyed genesis")
	}

	sdb := c.state()
	c.repairAll(sdb, c.head(), tkAccount)
	root, update, err := sdb.CommitWithUpdate(1, true, true)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(update.storages[crypto.Keccak256Hash(tkAccount[:])]); n != 0 {
		t.Errorf("a repair that only moved trie entries recorded %d flat-state changes", n)
	}
	if err := c.tdb.Commit(root, false); err != nil {
		t.Fatal(err)
	}
	c.roots = append(c.roots, root)
	c.assertTrieEqualsFlat(root, tkAccount)
	after := c.state()
	for i := range slots {
		if got := after.GetState(tkAccount, slots[i]); got != values[i] {
			t.Errorf("read of slot %x after the repair: %x, want %x", slots[i], got, values[i])
		}
	}
}

// TestARawReadSeesATrieKeyWrite: in the state that made it, a trie-key write is what a raw-key read of
// the slot returns.
func TestARawReadSeesATrieKeyWrite(t *testing.T) {
	c := newTKChain(t)
	c.genesis(nil)
	slot := common.HexToHash("0x07")
	sdb := c.state()
	if err := sdb.SetStorageByTrieKey(tkAccount, crypto.Keccak256Hash(slot[:]), common.HexToHash("0x99")); err != nil {
		t.Fatal(err)
	}
	if got := sdb.GetState(tkAccount, slot); got != common.HexToHash("0x99") {
		t.Errorf("raw read in the same state: %x, want 0x99", got)
	}
	if got := sdb.GetCommittedState(tkAccount, slot); got != common.HexToHash("0x99") {
		t.Errorf("committed-state read in the same state: %x, want 0x99", got)
	}
	root := c.commit(sdb)
	if got := c.state().GetState(tkAccount, slot); got != common.HexToHash("0x99") {
		t.Errorf("raw read at the committed root %x: %x, want 0x99", root, got)
	}
}

// TestARawWriteAfterATrieKeyWriteWins: a raw-key write that follows a trie-key write to the same slot
// decides the slot in the trie and the flat state alike, including a raw write back to zero, which is
// the case where a flat entry from the trie-key write would otherwise survive a trie deletion.
func TestARawWriteAfterATrieKeyWriteWins(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  common.Hash
	}{
		{name: "a raw write of another value", raw: common.HexToHash("0x02")},
		{name: "a raw write back to zero", raw: common.Hash{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			slot := common.HexToHash("0x05")
			for _, refresh := range []bool{false, true} {
				c := newTKChain(t)
				c.genesis(nil)
				sdb := c.state()
				if err := sdb.SetStorageByTrieKey(tkAccount, crypto.Keccak256Hash(slot[:]), common.HexToHash("0x01")); err != nil {
					t.Fatal(err)
				}
				if refresh {
					// An intermediate root between the two writes, as the node's block path takes.
					sdb.IntermediateRoot(true)
				}
				sdb.SetState(tkAccount, slot, tc.raw)
				root := c.commit(sdb)
				if got := c.state().GetState(tkAccount, slot); got != tc.raw {
					t.Errorf("refresh=%v: read after commit %x, want %x", refresh, got, tc.raw)
				}
				c.assertTrieEqualsFlat(root, tkAccount)
			}
		})
	}
}

// TestATrieKeyWriteAfterARawAccessIsRefused: the ordering precondition, and an absent account.
func TestATrieKeyWriteAfterARawAccessIsRefused(t *testing.T) {
	c := newTKChain(t)
	c.genesis(nil)
	key := crypto.Keccak256Hash([]byte{1})

	sdb := c.state()
	sdb.GetState(tkAccount, common.HexToHash("0x01"))
	if err := sdb.SetStorageByTrieKey(tkAccount, key, common.HexToHash("0x01")); err == nil || !strings.Contains(err.Error(), "raw key") {
		t.Errorf("a trie-key write after a raw read: err=%v", err)
	}
	sdb = c.state()
	sdb.SetState(tkAccount, common.HexToHash("0x01"), common.HexToHash("0x02"))
	if err := sdb.SetStorageByTrieKey(tkAccount, key, common.HexToHash("0x01")); err == nil {
		t.Error("a trie-key write after a raw write was accepted")
	}
	sdb = c.state()
	if err := sdb.SetStorageByTrieKey(common.HexToAddress("0xdead"), key, common.HexToHash("0x01")); err == nil {
		t.Error("a trie-key write to an absent account was accepted")
	}
	// A second trie-key write after a first is not a raw access.
	sdb = c.state()
	if err := sdb.SetStorageByTrieKey(tkAccount, key, common.HexToHash("0x01")); err != nil {
		t.Fatal(err)
	}
	if err := sdb.SetStorageByTrieKey(tkAccount, key, common.HexToHash("0x03")); err != nil {
		t.Errorf("a second trie-key write: %v", err)
	}
}

// TestACopiedStateCarriesTrieKeyWrites: Copy carries pending trie-key writes, and the copy commits the
// same root as the original.
func TestACopiedStateCarriesTrieKeyWrites(t *testing.T) {
	c := newTKChain(t)
	c.genesis(nil)
	sdb := c.state()
	if err := sdb.SetStorageByTrieKey(tkAccount, crypto.Keccak256Hash([]byte{9}), common.HexToHash("0x09")); err != nil {
		t.Fatal(err)
	}
	cp := sdb.Copy()
	if got, want := cp.IntermediateRoot(true), sdb.IntermediateRoot(true); got != want {
		t.Errorf("copy root %x, original %x", got, want)
	}
}

// TestAFlatRebuildAfterATrieKeyRepairLeavesReadsUnchanged: the pathdb generator rebuilds the flat state
// from the trie when its marker is lost. On the mis-keyed genesis that zeroes every genesis slot (the
// control); after the repair it changes nothing.
func TestAFlatRebuildAfterATrieKeyRepairLeavesReadsUnchanged(t *testing.T) {
	for _, repair := range []bool{false, true} {
		c := newTKChain(t)
		slots, values, storage := misKeyedGenesis()
		c.genesis(storage)
		if repair {
			sdb := c.state()
			c.repairAll(sdb, c.head(), tkAccount)
			c.commit(sdb)
		}
		c.reopen()
		rawdb.DeleteSnapshotGenerator(c.disk)
		c.reopen()
		sdb := c.state()
		for i := range slots {
			got := sdb.GetState(tkAccount, slots[i])
			switch {
			case repair && got != values[i]:
				t.Errorf("after the repair and a flat rebuild, slot %x reads %x, want %x", slots[i], got, values[i])
			case !repair && got != (common.Hash{}):
				t.Errorf("control: without the repair a flat rebuild should zero slot %x, read %x; the fixture "+
					"no longer reproduces the defect", slots[i], got)
			}
		}
	}
}

// TestRecoverAcrossATrieKeyWriteFailsAndCorruptsNothing: raw-key state history has no record of a
// trie-key write, so a rollback across the block that made it fails before writing, the head stays
// readable, and a rollback that does not cross it still works. After the repair, a rollback across the
// first raw write to a genesis slot works, which on the mis-keyed genesis it does not.
func TestRecoverAcrossATrieKeyWriteFailsAndCorruptsNothing(t *testing.T) {
	c := newTKChain(t)
	slots, values, storage := misKeyedGenesis()
	c.genesis(storage)

	// Block 1: an unrelated change, so block 2 has a history entry to roll back to.
	sdb := c.state()
	sdb.AddBalance(tkOther, uint256.NewInt(1), tracing.BalanceChangeUnspecified)
	c.commit(sdb)
	// Block 2: the repair.
	sdb = c.state()
	c.repairAll(sdb, c.head(), tkAccount)
	repaired := c.commit(sdb)
	// Block 3: unrelated.
	sdb = c.state()
	sdb.AddBalance(tkOther, uint256.NewInt(1), tracing.BalanceChangeUnspecified)
	c.commit(sdb)
	// Block 4: the first raw write to a genesis slot.
	sdb = c.state()
	sdb.SetState(tkAccount, slots[0], common.HexToHash("0xcc"))
	c.commit(sdb)

	// Across block 4 only: works.
	if err := c.tdb.Recover(c.roots[3]); err != nil {
		t.Fatalf("a rollback across the first raw write to a repaired genesis slot failed: %v", err)
	}
	c.roots = c.roots[:4]
	if got := c.state().GetState(tkAccount, slots[0]); got != values[0] {
		t.Errorf("after the rollback slot %x reads %x, want %x", slots[0], got, values[0])
	}
	// Across block 2, the repair: block 3 is reverted, block 2's revert fails, and the state the failed
	// step started from, block 2's, stays readable and consistent.
	err := c.tdb.Recover(c.roots[1])
	if err == nil {
		t.Fatal("a rollback across the block that wrote by trie key succeeded")
	}
	if !strings.Contains(err.Error(), "failed to reset storage trie") {
		t.Errorf("rollback error %q, want the storage root mismatch", err)
	}
	t.Logf("MEASURED rollback across the repair: %v", err)
	sdb, err = New(c.roots[2], NewDatabase(c.tdb, nil))
	if err != nil {
		t.Fatalf("state at the root the failed rollback started from is not readable: %v", err)
	}
	for i := range slots {
		if got := sdb.GetState(tkAccount, slots[i]); got != values[i] {
			t.Errorf("after the failed rollback slot %x reads %x, want %x", slots[i], got, values[i])
		}
	}
	c.assertTrieEqualsFlat(c.roots[2], tkAccount)
	if c.roots[2] != repaired {
		t.Fatal("fixture: the roots are not indexed by block")
	}
}
