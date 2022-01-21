package state

import (
	"math/big"

	"github.com/ethereum/evmc/v10/bindings/go/evmc"
	iradix "github.com/hashicorp/go-immutable-radix"
	"github.com/umbracle/go-web3"

	"github.com/0xPolygon/eth-state-transition/runtime"
	"github.com/0xPolygon/eth-state-transition/types"
)

var EmptyStateHash = types.StringToHash("0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421")

var (
	// logIndex is the index of the logs in the trie
	logIndex = types.BytesToHash([]byte{2}).Bytes()

	// refundIndex is the index of the refund
	refundIndex = types.BytesToHash([]byte{3}).Bytes()
)

// Txn is a reference of the state
type Txn struct {
	snapshot  Snapshot
	snapshots []*iradix.Tree
	txn       *iradix.Txn
	rev       evmc.Revision
}

func NewTxn(snapshot Snapshot) *Txn {
	return newTxn(snapshot)
}

func newTxn(snapshot Snapshot) *Txn {
	i := iradix.New()

	return &Txn{
		snapshot:  snapshot,
		snapshots: []*iradix.Tree{},
		txn:       i.Txn(),
	}
}

// Snapshot takes a snapshot at this point in time
func (txn *Txn) Snapshot() int {
	t := txn.txn.CommitOnly()

	id := len(txn.snapshots)
	txn.snapshots = append(txn.snapshots, t)

	// fmt.Printf("take snapshot ========> %d\n", id)

	return id
}

// RevertToSnapshot reverts to a given snapshot
func (txn *Txn) RevertToSnapshot(id int) {
	// fmt.Printf("revert to snapshot ======> %d\n", id)

	if id > len(txn.snapshots) {
		panic("")
	}

	tree := txn.snapshots[id]
	txn.txn = tree.Txn()
}

// GetAccount returns an account
func (txn *Txn) GetAccount(addr types.Address) (*Account, bool) {
	object, exists := txn.getStateObject(addr)
	if !exists {
		return nil, false
	}
	return object.Account, true
}

func (txn *Txn) getStateObject(addr types.Address) (*stateObject, bool) {
	// Try to get state from radix tree which holds transient states during block processing first
	val, exists := txn.txn.Get(addr.Bytes())
	if exists {
		obj := val.(*stateObject)
		if obj.Deleted {
			return nil, false
		}
		//fmt.Println("- get account 2 --")
		//fmt.Println(addr, obj.Account.Root)
		return obj.Copy(), true
	}

	account, err := txn.snapshot.GetAccount(addr)
	if err != nil {
		return nil, false
	}
	if account == nil {
		return nil, false
	}

	//fmt.Println("-- get account --")
	//fmt.Println(account)
	//fmt.Println(addr, account.Root)

	obj := &stateObject{
		Account: account.Copy(),
	}
	return obj, true
}

func (txn *Txn) upsertAccount(addr types.Address, create bool, f func(object *stateObject)) {
	object, exists := txn.getStateObject(addr)
	if !exists && create {
		object = &stateObject{
			Account: &Account{
				Balance:  big.NewInt(0),
				CodeHash: EmptyCodeHash[:],
				Root:     EmptyStateHash,
			},
		}
	}

	// run the callback to modify the account
	f(object)

	if object != nil {
		txn.txn.Insert(addr.Bytes(), object)
	}
}

func (txn *Txn) AddSealingReward(addr types.Address, balance *big.Int) {
	txn.upsertAccount(addr, true, func(object *stateObject) {
		if object.Suicide {
			*object = *newStateObject(txn)
			object.Account.Balance.SetBytes(balance.Bytes())
		} else {
			object.Account.Balance.Add(object.Account.Balance, balance)
		}
	})
}

// AddBalance adds balance
func (txn *Txn) AddBalance(addr types.Address, balance *big.Int) {
	txn.upsertAccount(addr, true, func(object *stateObject) {
		object.Account.Balance.Add(object.Account.Balance, balance)
	})
}

// SubBalance reduces the balance at address addr by amount
func (txn *Txn) SubBalance(addr types.Address, amount *big.Int) error {
	// If we try to reduce balance by 0, then it's a noop
	if amount.Sign() == 0 {
		return nil
	}

	// Check if we have enough balance to deduce amount from
	if balance := txn.GetBalance(evmc.Address(addr)); balance.Cmp(amount) < 0 {
		return runtime.ErrNotEnoughFunds
	}

	txn.upsertAccount(addr, true, func(object *stateObject) {
		object.Account.Balance.Sub(object.Account.Balance, amount)
	})

	return nil
}

// SetBalance sets the balance
func (txn *Txn) SetBalance(addr types.Address, balance *big.Int) {
	//fmt.Printf("SET BALANCE: %s %s\n", addr.String(), balance.String())
	txn.upsertAccount(addr, true, func(object *stateObject) {
		object.Account.Balance.SetBytes(balance.Bytes())
	})
}

// GetBalance returns the balance of an address
func (txn *Txn) GetBalance(addr evmc.Address) *big.Int {
	object, exists := txn.getStateObject(types.Address(addr))
	if !exists {
		return big.NewInt(0)
	}
	return object.Account.Balance
}

func (txn *Txn) EmitLog(addr evmc.Address, topics []evmc.Hash, data []byte) {
	log := &Log{
		Address: types.Address(addr),
	}
	for _, t := range topics {
		log.Topics = append(log.Topics, types.Hash(t))
	}
	log.Data = append(log.Data, data...)

	var logs []*Log
	val, exists := txn.txn.Get(logIndex)
	if !exists {
		logs = []*Log{}
	} else {
		logs = val.([]*Log)
	}

	logs = append(logs, log)
	txn.txn.Insert(logIndex, logs)
}

// State

var zeroHash types.Hash

func (txn *Txn) isRevision(rev evmc.Revision) bool {
	return rev <= txn.rev
}

func (txn *Txn) SetStorage(addr evmc.Address, key evmc.Hash, value evmc.Hash) (status evmc.StorageStatus) {
	oldValue := txn.GetState(evmc.Address(addr), evmc.Hash(key))
	if oldValue == value {
		return evmc.StorageUnchanged
	}

	current := oldValue                                                     // current - storage dirtied by previous lines of this contract
	original := txn.GetCommittedState(types.Address(addr), types.Hash(key)) // storage slot before this transaction started

	txn.SetState(types.Address(addr), types.Hash(key), types.Hash(value))

	isIstanbul := txn.isRevision(evmc.Istanbul)
	legacyGasMetering := !isIstanbul && (txn.isRevision(evmc.Petersburg) || !txn.isRevision(evmc.Constantinople))

	if legacyGasMetering {
		status = evmc.StorageModified
		if types.Hash(oldValue) == zeroHash {
			return evmc.StorageAdded
		} else if types.Hash(value) == zeroHash {
			txn.AddRefund(15000)
			return evmc.StorageDeleted
		}
		return evmc.StorageModified
	}

	if evmc.Hash(original) == current {
		if original == zeroHash { // create slot (2.1.1)
			return evmc.StorageAdded
		}
		if types.Hash(value) == zeroHash { // delete slot (2.1.2b)
			txn.AddRefund(15000)
			return evmc.StorageDeleted
		}
		return evmc.StorageModified
	}
	if original != zeroHash { // Storage slot was populated before this transaction started
		if types.Hash(current) == zeroHash { // recreate slot (2.2.1.1)
			txn.SubRefund(15000)
		} else if types.Hash(value) == zeroHash { // delete slot (2.2.1.2)
			txn.AddRefund(15000)
		}
	}
	if evmc.Hash(original) == value {
		if original == zeroHash { // reset to original nonexistent slot (2.2.2.1)
			// Storage was used as memory (allocation and deallocation occurred within the same contract)
			if isIstanbul {
				txn.AddRefund(19200)
			} else {
				txn.AddRefund(19800)
			}
		} else { // reset to original existing slot (2.2.2.2)
			if isIstanbul {
				txn.AddRefund(4200)
			} else {
				txn.AddRefund(4800)
			}
		}
	}
	return evmc.StorageModifiedAgain
}

// SetState change the state of an address
func (txn *Txn) SetState(addr types.Address, key, value types.Hash) {
	txn.upsertAccount(addr, true, func(object *stateObject) {
		if object.Txn == nil {
			object.Txn = iradix.New().Txn()
		}

		if value == zeroHash {
			object.Txn.Insert(key.Bytes(), nil)
		} else {
			object.Txn.Insert(key.Bytes(), value.Bytes())
		}
	})
}

// GetState returns the state of the address at a given key
func (txn *Txn) GetState(addr evmc.Address, key evmc.Hash) evmc.Hash {
	object, exists := txn.getStateObject(types.Address(addr))
	if !exists {
		return evmc.Hash{}
	}

	// Try to get account state from radix tree first
	// Because the latest account state should be in in-memory radix tree
	// if account state update happened in previous transactions of same block
	if object.Txn != nil {
		if val, ok := object.Txn.Get(key[:]); ok {
			if val == nil {
				return evmc.Hash{}
			}
			return evmc.Hash(types.BytesToHash(val.([]byte)))
		}
	}
	//fmt.Println("-- get storage 1", types.Address(addr), object.Account.Root)
	return evmc.Hash(txn.snapshot.GetStorage(types.Address(addr), object.Account.Root, types.Hash(key)))
}

// Nonce

// IncrNonce increases the nonce of the address
func (txn *Txn) IncrNonce(addr types.Address) {
	txn.upsertAccount(addr, true, func(object *stateObject) {
		object.Account.Nonce++
	})
}

// SetNonce reduces the balance
func (txn *Txn) SetNonce(addr types.Address, nonce uint64) {
	txn.upsertAccount(addr, true, func(object *stateObject) {
		object.Account.Nonce = nonce
	})
}

// GetNonce returns the nonce of an addr
func (txn *Txn) GetNonce(addr types.Address) uint64 {
	object, exists := txn.getStateObject(addr)
	if !exists {
		return 0
	}
	return object.Account.Nonce
}

// Code

// SetCode sets the code for an address
func (txn *Txn) SetCode(addr types.Address, code []byte) {
	txn.upsertAccount(addr, true, func(object *stateObject) {
		object.Account.CodeHash = web3.Keccak256(code)
		object.DirtyCode = true
		object.Code = code
	})
}

func (txn *Txn) GetCode(addr evmc.Address) []byte {
	object, exists := txn.getStateObject(types.Address(addr))
	if !exists {
		return nil
	}
	if object.DirtyCode {
		return object.Code
	}
	code, _ := txn.snapshot.GetCode(types.BytesToHash(object.Account.CodeHash), types.Address(addr))
	return code
}

func (txn *Txn) GetCodeSize(addr evmc.Address) int {
	return len(txn.GetCode(addr))
}

func (txn *Txn) GetCodeHash(addr evmc.Address) types.Hash {
	if txn.Empty(addr) {
		return types.Hash{}
	}
	object, exists := txn.getStateObject(types.Address(addr))
	if !exists {
		return types.Hash{}
	}
	return types.BytesToHash(object.Account.CodeHash)
}

// Suicide marks the given account as suicided
func (txn *Txn) Suicide(addr types.Address) bool {
	var suicided bool
	txn.upsertAccount(addr, false, func(object *stateObject) {
		if object == nil || object.Suicide {
			suicided = false
		} else {
			suicided = true
			object.Suicide = true
		}
		if object != nil {
			object.Account.Balance = new(big.Int)
		}
	})
	return suicided
}

// HasSuicided returns true if the account suicided
func (txn *Txn) HasSuicided(addr types.Address) bool {
	object, exists := txn.getStateObject(addr)
	return exists && object.Suicide
}

// Refund
func (txn *Txn) AddRefund(gas uint64) {
	// fmt.Printf("=-----------ADD REFUND: %d\n", gas)

	refund := txn.GetRefund() + gas
	txn.txn.Insert(refundIndex, refund)
}

func (txn *Txn) SubRefund(gas uint64) {
	refund := txn.GetRefund() - gas
	txn.txn.Insert(refundIndex, refund)
}

func (txn *Txn) Logs() []*Log {
	data, exists := txn.txn.Get(logIndex)
	if !exists {
		return nil
	}
	txn.txn.Delete(logIndex)
	return data.([]*Log)
}

func (txn *Txn) GetRefund() uint64 {
	data, exists := txn.txn.Get(refundIndex)
	if !exists {
		return 0
	}
	return data.(uint64)
}

// GetCommittedState returns the state of the address in the trie
func (txn *Txn) GetCommittedState(addr types.Address, key types.Hash) types.Hash {
	obj, ok := txn.getStateObject(addr)
	if !ok {
		return types.Hash{}
	}
	//fmt.Println("-- get storage 2", addr, obj.Account.Root)
	return txn.snapshot.GetStorage(addr, obj.Account.Root, key)
}

func (txn *Txn) TouchAccount(addr types.Address) {
	txn.upsertAccount(addr, true, func(obj *stateObject) {

	})
}

// TODO, check panics with this ones

func (txn *Txn) Exist(addr evmc.Address) bool {
	_, exists := txn.getStateObject(types.Address(addr))
	return exists
}

func (txn *Txn) Empty(addr evmc.Address) bool {
	obj, exists := txn.getStateObject(types.Address(addr))
	if !exists {
		return true
	}
	return obj.Empty()
}

func newStateObject(txn *Txn) *stateObject {
	return &stateObject{
		Account: &Account{
			Balance:  big.NewInt(0),
			CodeHash: EmptyCodeHash[:],
			Root:     EmptyStateHash,
		},
	}
}

func (txn *Txn) CreateAccount(addr types.Address) {
	obj := &stateObject{
		Account: &Account{
			Balance:  big.NewInt(0),
			CodeHash: EmptyCodeHash[:],
			Root:     EmptyStateHash,
		},
	}

	prev, ok := txn.getStateObject(addr)
	if ok {
		obj.Account.Balance.SetBytes(prev.Account.Balance.Bytes())
	}

	txn.txn.Insert(addr.Bytes(), obj)
}

func (txn *Txn) CleanDeleteObjects(deleteEmptyObjects bool) {
	remove := [][]byte{}
	txn.txn.Root().Walk(func(k []byte, v interface{}) bool {
		a, ok := v.(*stateObject)
		if !ok {
			return false
		}
		if a.Suicide || a.Empty() && deleteEmptyObjects {
			remove = append(remove, k)
		}
		return false
	})

	for _, k := range remove {
		v, ok := txn.txn.Get(k)
		if !ok {
			panic("it should not happen")
		}
		obj, ok := v.(*stateObject)
		if !ok {
			panic("it should not happen")
		}

		obj2 := obj.Copy()
		obj2.Deleted = true
		txn.txn.Insert(k, obj2)
	}

	// delete refunds
	txn.txn.Delete(refundIndex)
}

func (txn *Txn) Commit() []*Object {
	// txn.CleanDeleteObjects(deleteEmptyObjects)

	x := txn.txn.Commit()

	// Do a more complex thing for now
	objs := []*Object{}
	x.Root().Walk(func(k []byte, v interface{}) bool {
		a, ok := v.(*stateObject)
		if !ok {
			// We also have logs, avoid those
			return false
		}

		obj := &Object{
			Nonce:     a.Account.Nonce,
			Address:   types.BytesToAddress(k),
			Balance:   a.Account.Balance,
			Root:      a.Account.Root,
			CodeHash:  types.BytesToHash(a.Account.CodeHash),
			DirtyCode: a.DirtyCode,
			Code:      a.Code,
		}
		if a.Deleted {
			obj.Deleted = true
		} else {
			if a.Txn != nil {
				a.Txn.Root().Walk(func(k []byte, v interface{}) bool {
					store := &StorageObject{Key: k}
					if v == nil {
						store.Deleted = true
					} else {
						store.Val = v.([]byte)
					}
					obj.Storage = append(obj.Storage, store)
					return false
				})
			}
		}

		objs = append(objs, obj)
		return false
	})

	return objs
}
