package storage

import (
	"errors"
	"sync"

	"github.com/mailhog/data"
)

// InMemory is an in memory storage backend
type InMemory struct {
	MessageIDIndex map[string]int
	Messages       []*data.Message
	mu             sync.Mutex
}

// CreateInMemory creates a new in memory storage backend
func CreateInMemory() *InMemory {
	return &InMemory{
		MessageIDIndex: make(map[string]int),
		Messages:       make([]*data.Message, 0),
	}
}

// Store stores a message and returns its storage ID
func (memory *InMemory) Store(m *data.Message) (string, error) {
	memory.mu.Lock()
	defer memory.mu.Unlock()
	memory.Messages = append(memory.Messages, m)
	memory.MessageIDIndex[string(m.ID)] = len(memory.Messages) - 1
	return string(m.ID), nil
}

// Count returns the number of stored messages
func (memory *InMemory) Count() int {
	return len(memory.Messages)
}

// Search finds messages matching the query, newest first.
//
// It returns the requested window of matches together with the total number of
// matching messages, which is independent of the window. See query.go for the
// shared listing, sorting and windowing semantics.
func (memory *InMemory) Search(kind, query string, start, limit int) (*data.Messages, int, error) {
	memory.mu.Lock()
	defer memory.mu.Unlock()

	matched := make([]data.Message, 0)
	for _, m := range memory.Messages {
		if matches(m, kind, query) {
			matched = append(matched, *m)
		}
	}

	sortByCreatedDesc(matched)
	page := window(matched, start, limit)

	msgs := data.Messages(page)
	return &msgs, len(matched), nil
}

// List lists stored messages, newest first, returning the requested window.
func (memory *InMemory) List(start int, limit int) (*data.Messages, error) {
	memory.mu.Lock()
	defer memory.mu.Unlock()

	all := make([]data.Message, 0, len(memory.Messages))
	for _, m := range memory.Messages {
		all = append(all, *m)
	}

	sortByCreatedDesc(all)
	page := window(all, start, limit)

	msgs := data.Messages(page)
	return &msgs, nil
}

// DeleteOne deletes an individual message by storage ID
func (memory *InMemory) DeleteOne(id string) error {
	memory.mu.Lock()
	defer memory.mu.Unlock()

	var index int
	var ok bool

	if index, ok = memory.MessageIDIndex[id]; !ok && true {
		return errors.New("message not found")
	}

	delete(memory.MessageIDIndex, id)
	for k, v := range memory.MessageIDIndex {
		if v > index {
			memory.MessageIDIndex[k] = v - 1
		}
	}
	memory.Messages = append(memory.Messages[:index], memory.Messages[index+1:]...)
	return nil
}

// DeleteAll deletes all in memory messages
func (memory *InMemory) DeleteAll() error {
	memory.mu.Lock()
	defer memory.mu.Unlock()
	memory.Messages = make([]*data.Message, 0)
	memory.MessageIDIndex = make(map[string]int)
	return nil
}

// Load returns an individual message by storage ID
func (memory *InMemory) Load(id string) (*data.Message, error) {
	if idx, ok := memory.MessageIDIndex[id]; ok {
		return memory.Messages[idx], nil
	}
	return nil, nil
}
