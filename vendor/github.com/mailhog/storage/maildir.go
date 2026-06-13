package storage

import (
	"io/ioutil"
	"log"
	"os"
	"path/filepath"

	"github.com/mailhog/data"
)

// Maildir is a maildir storage backend
type Maildir struct {
	Path string
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
	b, err := ioutil.ReadAll(m.Raw.Bytes())
	if err != nil {
		return "", err
	}
	err = ioutil.WriteFile(filepath.Join(maildir.Path, string(m.ID)), b, 0660)
	return string(m.ID), err
}

// Count returns the number of stored messages
func (maildir *Maildir) Count() int {
	// FIXME may be wrong, ../. ?
	// and handle error?
	dir, err := os.Open(maildir.Path)
	if err != nil {
		panic(err)
	}
	defer dir.Close()
	n, _ := dir.Readdirnames(0)
	return len(n)
}

// readAll loads and parses every message stored in the maildir.
//
// A message's Created timestamp is taken from its file modification time, which
// is what gives the maildir backend a stable newest-first ordering once the
// results are sorted.
func (maildir *Maildir) readAll() ([]data.Message, error) {
	dir, err := os.Open(maildir.Path)
	if err != nil {
		return nil, err
	}
	defer dir.Close()

	infos, err := dir.Readdir(0)
	if err != nil {
		return nil, err
	}

	messages := make([]data.Message, 0, len(infos))
	for _, info := range infos {
		if info.IsDir() {
			continue
		}
		b, err := ioutil.ReadFile(filepath.Join(maildir.Path, info.Name()))
		if err != nil {
			return nil, err
		}
		// FIXME domain
		m := *data.FromBytes(b).Parse("mailhog.example")
		m.ID = data.MessageID(info.Name())
		m.Created = info.ModTime()
		messages = append(messages, m)
	}

	return messages, nil
}

// Search finds messages matching the query, newest first.
//
// It returns the requested window of matches together with the total number of
// matching messages, which is independent of the window. See query.go for the
// shared listing, sorting and windowing semantics.
//
// Like the in-memory backend it scans every stored message, which is required
// to compute an accurate total and a correctly ordered window from a flat
// maildir.
func (maildir *Maildir) Search(kind, query string, start, limit int) (*data.Messages, int, error) {
	messages, err := maildir.readAll()
	if err != nil {
		log.Println(err)
		return nil, 0, err
	}

	matched := make([]data.Message, 0)
	for i := range messages {
		if matches(&messages[i], kind, query) {
			matched = append(matched, messages[i])
		}
	}

	sortByCreatedDesc(matched)
	page := window(matched, start, limit)

	msgs := data.Messages(page)
	return &msgs, len(matched), nil
}

// List lists stored messages, newest first, returning the requested window.
func (maildir *Maildir) List(start, limit int) (*data.Messages, error) {
	messages, err := maildir.readAll()
	if err != nil {
		return nil, err
	}

	sortByCreatedDesc(messages)
	page := window(messages, start, limit)

	msgs := data.Messages(page)
	return &msgs, nil
}

// DeleteOne deletes an individual message by storage ID
func (maildir *Maildir) DeleteOne(id string) error {
	return os.Remove(filepath.Join(maildir.Path, id))
}

// DeleteAll deletes all in memory messages
func (maildir *Maildir) DeleteAll() error {
	err := os.RemoveAll(maildir.Path)
	if err != nil {
		return err
	}
	return os.Mkdir(maildir.Path, 0770)
}

// Load returns an individual message by storage ID
func (maildir *Maildir) Load(id string) (*data.Message, error) {
	b, err := ioutil.ReadFile(filepath.Join(maildir.Path, id))
	if err != nil {
		return nil, err
	}
	// FIXME domain
	m := data.FromBytes(b).Parse("mailhog.example")
	m.ID = data.MessageID(id)
	return m, nil
}
