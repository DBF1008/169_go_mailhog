package storage

import (
	"errors"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

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

// Search finds messages matching the query
func (maildir *Maildir) Search(kind, query string, start, limit int) (*data.Messages, int, error) {
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

		msg, err := maildir.Load(info.Name())
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

// List lists stored messages by index, newest first.
func (maildir *Maildir) List(start, limit int) (*data.Messages, error) {
	log.Println("Listing messages in", maildir.Path)
	messages := make([]data.Message, 0)

	dir, err := os.Open(maildir.Path)
	if err != nil {
		return nil, err
	}
	defer dir.Close()

	infos, err := dir.Readdir(0)
	if err != nil {
		return nil, err
	}

	// Only consider regular files. A maildir directory may legitimately
	// contain subdirectories (e.g. cur/new/tmp) or unrelated hidden files
	// which are not MailHog messages; including them previously caused the
	// whole listing to fail with a 500.
	files := make([]os.FileInfo, 0, len(infos))
	for _, info := range infos {
		if info.IsDir() {
			continue
		}
		files = append(files, info)
	}

	// Sort newest first so the ordering matches the in-memory and MongoDB
	// backends (the UI expects the most recent messages first). Fall back to
	// the file name to keep the ordering deterministic when modification
	// times collide.
	sort.Slice(files, func(i, j int) bool {
		ti, tj := files[i].ModTime(), files[j].ModTime()
		if ti.Equal(tj) {
			return files[i].Name() > files[j].Name()
		}
		return ti.After(tj)
	})

	// Apply the requested window, mirroring the semantics of the other
	// storage backends.
	if start < 0 {
		start = 0
	}
	if start >= len(files) {
		msgs := data.Messages(messages)
		return &msgs, nil
	}
	end := len(files)
	if limit > 0 && start+limit < end {
		end = start + limit
	}

	for _, fileinfo := range files[start:end] {
		b, err := ioutil.ReadFile(filepath.Join(maildir.Path, fileinfo.Name()))
		if err != nil {
			// A single unreadable message must not break the whole listing.
			log.Printf("Error reading message %s: %s", fileinfo.Name(), err)
			continue
		}
		msg := data.FromBytes(b)
		// FIXME domain
		m := *msg.Parse("mailhog.example")
		m.ID = data.MessageID(fileinfo.Name())
		m.Created = fileinfo.ModTime()
		messages = append(messages, m)
	}

	log.Printf("Found %d messages", len(messages))
	msgs := data.Messages(messages)
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
	path := filepath.Join(maildir.Path, id)

	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			// Match the in-memory backend: a missing message is not an error.
			// Callers (and the HTTP API) then return an empty result instead
			// of a 500, e.g. when a message was just deleted.
			return nil, nil
		}
		return nil, err
	}
	if info.IsDir() {
		return nil, nil
	}

	b, err := ioutil.ReadFile(path)
	if err != nil {
		return nil, err
	}
	// FIXME domain
	m := data.FromBytes(b).Parse("mailhog.example")
	m.ID = data.MessageID(id)
	return m, nil
}
