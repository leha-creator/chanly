// Package storagefake — хранилище в памяти для тестов (07 §9).
package storagefake

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/url"
	"sync"
	"time"

	"github.com/leha-creator/chanly/internal/platform/storage"
)

// PublicBaseURL — адрес публичной области поддельного хранилища.
const PublicBaseURL = "https://public.storage.test"

// Операции для FailWith.
const (
	OpPut          = "PutPrivate"
	OpGet          = "Get"
	OpPresign      = "PresignGet"
	OpDelete       = "Delete"
	OpCopyToPublic = "CopyToPublic"
)

// Object — сохранённый объект.
type Object struct {
	Data        []byte
	ContentType string
}

// Storage — реализация storage.ObjectStorage в памяти.
type Storage struct {
	mu      sync.Mutex
	objects map[string]Object
	public  map[string]Object
	fail    map[string]error
	copies  int
}

var _ storage.ObjectStorage = (*Storage)(nil)

// New создаёт пустое хранилище.
func New() *Storage {
	return &Storage{objects: map[string]Object{}, public: map[string]Object{}, fail: map[string]error{}}
}

// FailWith заставляет операцию op возвращать err; nil снимает отказ.
func (s *Storage) FailWith(op string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err == nil {
		delete(s.fail, op)
		return
	}
	s.fail[op] = err
}

func (s *Storage) PutPrivate(ctx context.Context, key string, r io.Reader, size int64, contentType string) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	if int64(len(data)) != size {
		return fmt.Errorf("storagefake: size %d, read %d bytes", size, len(data))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.fail[OpPut]; err != nil {
		return err
	}
	s.objects[key] = Object{Data: data, ContentType: contentType}
	return nil
}

func (s *Storage) Get(ctx context.Context, key string) (io.ReadCloser, int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.fail[OpGet]; err != nil {
		return nil, 0, err
	}
	o, ok := s.objects[key]
	if !ok {
		return nil, 0, storage.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(bytes.Clone(o.Data))), int64(len(o.Data)), nil
}

// PresignGet возвращает ссылку вида https://storage.test/<key>?expires=<секунды>.
func (s *Storage) PresignGet(ctx context.Context, key string, ttl time.Duration) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.fail[OpPresign]; err != nil {
		return "", err
	}
	if _, ok := s.objects[key]; !ok {
		return "", storage.ErrNotFound
	}
	return fmt.Sprintf("https://storage.test/%s?expires=%d", url.PathEscape(key), int(ttl.Seconds())), nil
}

func (s *Storage) Delete(ctx context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.fail[OpDelete]; err != nil {
		return err
	}
	delete(s.objects, key)
	return nil
}

func (s *Storage) CopyToPublic(ctx context.Context, privateKey, publicKey, contentType string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.fail[OpCopyToPublic]; err != nil {
		return err
	}
	o, ok := s.objects[privateKey]
	if !ok {
		return storage.ErrNotFound
	}
	s.public[publicKey] = Object{Data: bytes.Clone(o.Data), ContentType: contentType}
	s.copies++
	return nil
}

func (s *Storage) PublicURL(publicKey string) string { return PublicBaseURL + "/" + publicKey }

func (s *Storage) NewPublicKey(ext string) string { return storage.NewPublicKey(ext) }

// Object возвращает объект закрытой области по ключу.
func (s *Storage) Object(key string) (Object, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.objects[key]
	if ok {
		o.Data = bytes.Clone(o.Data)
	}
	return o, ok
}

// Public возвращает объект публичной области по ключу.
func (s *Storage) Public(key string) (Object, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.public[key]
	if ok {
		o.Data = bytes.Clone(o.Data)
	}
	return o, ok
}

// Len — число объектов закрытой области.
func (s *Storage) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.objects)
}

// Copies — сколько раз выполнялось копирование в публичную область.
func (s *Storage) Copies() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.copies
}
