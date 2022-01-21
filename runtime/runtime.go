package runtime

import (
	"errors"
	"math/big"

	"github.com/0xPolygon/eth-state-transition/types"
	"github.com/ethereum/evmc/v10/bindings/go/evmc"
)

// Host is the execution host
type Host interface {
	AccountExists(addr evmc.Address) bool
	GetStorage(addr evmc.Address, key evmc.Hash) evmc.Hash

	SetStorage(addr evmc.Address, key evmc.Hash, value evmc.Hash) evmc.StorageStatus
	GetBalance(addr evmc.Address) *big.Int
	GetCodeSize(addr evmc.Address) int
	GetCodeHash(addr evmc.Address) evmc.Hash
	GetCode(addr evmc.Address) []byte
	Selfdestruct(addr evmc.Address, beneficiary evmc.Address)
	GetTxContext() evmc.TxContext
	GetBlockHash(number int64) evmc.Hash
	EmitLog(addr evmc.Address, topics []evmc.Hash, data []byte)
	Callx(*Contract) ([]byte, int64, error)
	Empty(addr evmc.Address) bool

	Cally(kind evmc.CallKind,
		recipient types.Address, sender types.Address, value types.Hash, input []byte, gas int64, depth int,
		static bool, salt types.Hash, codeAddress types.Address) (output []byte, gasLeft int64, createAddr types.Address, err error)
}

var (
	ErrOutOfGas                 = errors.New("out of gas")
	ErrStackOverflow            = errors.New("stack overflow")
	ErrStackUnderflow           = errors.New("stack underflow")
	ErrNotEnoughFunds           = errors.New("not enough funds")
	ErrInsufficientBalance      = errors.New("insufficient balance for transfer")
	ErrMaxCodeSizeExceeded      = errors.New("evm: max code size exceeded")
	ErrContractAddressCollision = errors.New("contract address collision")
	ErrDepth                    = errors.New("max call depth exceeded")
	ErrExecutionReverted        = errors.New("execution was reverted")
	ErrCodeStoreOutOfGas        = errors.New("contract creation code storage out of gas")
)

// Contract is the instance being called
type Contract struct {
	Code        []byte
	Type        evmc.CallKind
	CodeAddress types.Address
	Address     types.Address
	Caller      types.Address
	Depth       int
	Value       *big.Int
	Input       []byte
	Gas         uint64
	Static      bool
	Salt        types.Hash
}

func NewContract(typ evmc.CallKind, depth int, from types.Address, to types.Address, value *big.Int, gas uint64, code []byte) *Contract {
	f := &Contract{
		Type:        typ,
		Caller:      from,
		CodeAddress: to,
		Address:     to,
		Gas:         gas,
		Value:       value,
		Code:        code,
		Depth:       depth,
	}
	return f
}

func NewContractCreation(depth int, from types.Address, to types.Address, value *big.Int, gas uint64, code []byte) *Contract {
	c := NewContract(evmc.Create, depth, from, to, value, gas, code)
	return c
}

func NewContractCall(depth int, from types.Address, to types.Address, value *big.Int, gas uint64, code []byte, input []byte) *Contract {
	c := NewContract(evmc.Call, depth, from, to, value, gas, code)
	c.Input = input
	return c
}
