package update

import (
	"encoding/json"
	"fmt"
	"github.com/pkg/errors"
	"go.etcd.io/bbolt"
	"time"
)

type (
	store struct {
		path string
	}
	session struct {
		db *bbolt.DB
	}
	bucket struct {
		b *bbolt.Bucket
	}
)

var (
	ErrUpdateNotFound = errors.New("Update not found")
)

const (
	UpdatesBucketName = "updates"
)

func newStore(dbFilePath string) (*store, error) {
	db, err := bbolt.Open(dbFilePath, 0600, bbolt.DefaultOptions)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	// Create the bucket if it doesn't already exist
	err = db.Update(func(tx *bbolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists([]byte(UpdatesBucketName))
		return err
	})

	if err != nil {
		return nil, err
	}

	return &store{path: dbFilePath}, nil
}

func (s *store) saveUpdate(u *Update) error {
	db, err := bbolt.Open(s.path, 0600, bbolt.DefaultOptions)
	if err != nil {
		return err
	}
	defer db.Close()

	return db.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(UpdatesBucketName))
		data, err := json.Marshal(u)
		if err != nil {
			return err
		}
		key := []byte(fmt.Sprintf("%020d:%store", u.CreationTime.UnixNano(), u.ID))
		return b.Put(key, data)
	})
}
func (s *store) lock(fn func(db *session) error) error {
	db, err := bbolt.Open(s.path, 0600, bbolt.DefaultOptions)
	if err != nil {
		return err
	}
	defer db.Close()
	return fn(&session{db})
}

func (s *session) write(u *Update) error {
	return s.db.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(UpdatesBucketName))
		bb := &bucket{b}
		return bb.write(u)
	})
}

func (b *bucket) write(u *Update) error {
	u.UpdateTime = time.Now()
	data, err := json.Marshal(u)
	if err != nil {
		return err
	}
	key, _ := b.b.Cursor().Last()
	if key == nil {
		panic("no key found in bucket, this should not happen")
	}
	return b.b.Put(key, data)
}

func (s *store) getCurrentUpdate() (*Update, error) {
	db, err := bbolt.Open(s.path, 0600, &bbolt.Options{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer db.Close()

	var foundUpdate *Update
	err = db.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(UpdatesBucketName))
		cursor := b.Cursor()

		// Iterate over all updates in the database and collect those that are non-completed or non-failed
		for k, v := cursor.First(); k != nil; k, v = cursor.Next() {
			var u Update
			err := json.Unmarshal(v, &u)
			if err != nil {
				return err
			}
			// Check if Update is in a non-completed or non-failed state
			if !(u.State == StateCompleted || u.State == StateFailed || u.State == StateCanceled) {
				foundUpdate = &u
				return nil
			}
		}
		return nil
	})

	if err != nil {
		return nil, err
	}
	if foundUpdate == nil {
		return nil, ErrUpdateNotFound
	}

	return foundUpdate, nil
}

func (s *store) getLastUpdate() (*Update, error) {
	db, err := bbolt.Open(s.path, 0600, &bbolt.Options{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer db.Close()

	var foundUpdate *Update
	err = db.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(UpdatesBucketName))
		cursor := b.Cursor()
		_, v := cursor.Last()
		var u Update
		err := json.Unmarshal(v, &u)
		if err != nil {
			return err
		}
		foundUpdate = &u

		return nil
	})

	if err != nil {
		return nil, err
	}
	if foundUpdate == nil {
		return nil, ErrUpdateNotFound
	}

	return foundUpdate, nil
}
