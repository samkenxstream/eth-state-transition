package itrie

import (
	"fmt"

	lru "github.com/hashicorp/golang-lru"
	"github.com/umbracle/minimal/state"
	"github.com/umbracle/minimal/types"
)

type State struct {
	storage Storage
	cache   *lru.Cache
}

func NewState(storage Storage) *State {
	cache, _ := lru.New(128)

	s := &State{
		storage: storage,
		cache:   cache,
	}
	return s
}

func (s *State) NewSnapshot() state.Snapshot {
	t := NewTrie()
	t.state = s
	t.storage = s.storage
	return t
}

func (s *State) SetCode(hash types.Hash, code []byte) {
	s.storage.SetCode(hash, code)
}

func (s *State) GetCode(hash types.Hash) ([]byte, bool) {
	return s.storage.GetCode(hash)
}

func (s *State) NewSnapshotAt(root types.Hash) (state.Snapshot, error) {
	tt, ok := s.cache.Get(root)
	if ok {
		t := tt.(*Trie)
		t.state = s
		return tt.(*Trie), nil
	}
	n, ok, err := GetNode(root.Bytes(), s.storage)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("not found")
	}
	t := &Trie{
		root:    n,
		state:   s,
		storage: s.storage,
	}
	return t, nil
}

func (s *State) AddState(root types.Hash, t *Trie) {
	s.cache.Add(root, t)
}
