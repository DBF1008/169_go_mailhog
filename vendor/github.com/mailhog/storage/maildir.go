package storage

import (
	"errors"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/mailhog/data"
)

// Maildir is a maildir storage backend
type Maildir struct {
	Path string
	mu   sync.Mutex
}

// CreateMaildir creates a new maildir storage backend
func CreateMaildir(path string) *Maildir {
	if len(path) == 0 {
		dir, err := ioutil.TempDir("", "mailhog")
		if err != nil {
			panic(err)
		}
		path = dir
	}
	if _, err := os.Stat(path); err != nil {
		err := os.MkdirAll(path, 0770)
		if err != nil {
			panic(err)
		}
	}
	log.Println("Maildir path is", path)
	return &Maildir{
		Path: path,
	}
}

// Store stores a message and returns its storage ID
func (maildir *Maildir) Store(m *data.Message) (string, error) {
	maildir.mu.Lock()
	defer maildir.mu.Unlock()

	b, err := ioutil.ReadAll(m.Raw.Bytes())
	if err != nil {
		return "", err
	}
	err = ioutil.WriteFile(filepath.Join(maildir.Path, string(m.ID)), b, 0660)
	return string(m.ID), err
}

// Count returns the number of stored messages
func (maildir *Maildir) Count() int {
	maildir.mu.Lock()
	defer maildir.mu.Unlock()

	dir, err := os.Open(maildir.Path)
	if err != nil {
		return 0
	}
	defer dir.Close()
	names, err := dir.Readdirnames(0)
	if err != nil {
		return 0
	}
	count := 0
	for _, name := range names {
		if name != "." && name != ".." {
			count++
		}
	}
	return count
}

// Search finds messages matching the query
func (maildir *Maildir) Search(kind, query string, start, limit int) (*data.Messages, int, error) {
	maildir.mu.Lock()
	defer maildir.mu.Unlock()

	query = strings.ToLower(query)
	var filteredMessages = make([]data.Message, 0)

	var matched int

	err := filepath.Walk(maildir.Path, func(path string, info os.FileInfo, err error) error {
		if limit > 0 && len(filteredMessages) >= limit {
			return errors.New("reached limit")
		}

		if info.IsDir() {
			return nil
		}

		msg, err := maildir.loadUnsafe(info.Name())
		if err != nil {
			log.Println(err)
			return nil
		}

		switch kind {
		case "to":
			for _, t := range msg.To {
				if strings.Contains(strings.ToLower(t.Mailbox+"@"+t.Domain), query) {
					if start > matched {
						matched++
						break
					}
					filteredMessages = append(filteredMessages, *msg)
					break
				}
			}
		case "from":
			if strings.Contains(strings.ToLower(msg.From.Mailbox+"@"+msg.From.Domain), query) {
				if start > matched {
					matched++
					break
				}
				filteredMessages = append(filteredMessages, *msg)
			}
		case "containing":
			if strings.Contains(strings.ToLower(msg.Raw.Data), query) {
				if start > matched {
					matched++
					break
				}
				filteredMessages = append(filteredMessages, *msg)
			}
		}

		return nil
	})

	if err != nil {
		log.Println(err)
	}

	msgs := data.Messages(filteredMessages)
	return &msgs, len(filteredMessages), nil
}

// List lists stored messages by index
func (maildir *Maildir) List(start, limit int) (*data.Messages, error) {
	maildir.mu.Lock()
	defer maildir.mu.Unlock()

	return maildir.listUnsafe(start, limit)
}

// listUnsafe lists messages without locking the mutex - caller must hold the lock
func (maildir *Maildir) listUnsafe(start, limit int) (*data.Messages, error) {
	messages := make([]data.Message, 0)

	dir, err := os.Open(maildir.Path)
	if err != nil {
		return nil, err
	}
	defer dir.Close()

	entries, err := dir.Readdir(0)
	if err != nil {
		return nil, err
	}

	for _, fileinfo := range entries {
		if fileinfo.IsDir() {
			continue
		}
		b, err := ioutil.ReadFile(filepath.Join(maildir.Path, fileinfo.Name()))
		if err != nil {
			log.Printf("Error reading message file %s: %s", fileinfo.Name(), err)
			continue
		}
		msg := data.FromBytes(b)
		// FIXME domain
		m := *msg.Parse("mailhog.example")
		m.ID = data.MessageID(fileinfo.Name())
		m.Created = fileinfo.ModTime()
		messages = append(messages, m)
	}

	// Sort by Created time descending (newest first), matching InMemory/MongoDB behavior
	sort.Slice(messages, func(i, j int) bool {
		return messages[i].Created.After(messages[j].Created)
	})

	// Apply pagination: messages are already sorted newest-first,
	// so start=0 means the newest message, matching InMemory/MongoDB behavior
	total := len(messages)
	if total == 0 || start >= total {
		empty := make([]data.Message, 0)
		msgs := data.Messages(empty)
		return &msgs, nil
	}

	end := start + limit
	if end > total {
		end = total
	}

	result := messages[start:end]
	msgs := data.Messages(result)
	return &msgs, nil
}

// DeleteOne deletes an individual message by storage ID
func (maildir *Maildir) DeleteOne(id string) error {
	maildir.mu.Lock()
	defer maildir.mu.Unlock()

	return os.Remove(filepath.Join(maildir.Path, id))
}

// DeleteAll deletes all stored messages
func (maildir *Maildir) DeleteAll() error {
	maildir.mu.Lock()
	defer maildir.mu.Unlock()

	err := os.RemoveAll(maildir.Path)
	if err != nil {
		return err
	}
	return os.Mkdir(maildir.Path, 0770)
}

// Load returns an individual message by storage ID
func (maildir *Maildir) Load(id string) (*data.Message, error) {
	maildir.mu.Lock()
	defer maildir.mu.Unlock()

	return maildir.loadUnsafe(id)
}

// loadUnsafe loads a message without locking the mutex - caller must hold the lock
func (maildir *Maildir) loadUnsafe(id string) (*data.Message, error) {
	b, err := ioutil.ReadFile(filepath.Join(maildir.Path, id))
	if err != nil {
		return nil, err
	}
	// FIXME domain
	m := data.FromBytes(b).Parse("mailhog.example")
	m.ID = data.MessageID(id)
	return m, nil
}
