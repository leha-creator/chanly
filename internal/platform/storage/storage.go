// Package storage — S3-совместимое объектное хранилище (07 §1, A-26).
// Закрытая область хранит оригиналы и документы; наружу они выдаются только
// кратковременными подписанными ссылками (07 §4). Публичная область — отдельная
// корзина с анонимным чтением без перечисления: туда копируются только производные
// изображений, вошедшие в версию поста (R-7).
package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/leha-creator/chanly/internal/platform/clock"
	"github.com/leha-creator/chanly/internal/platform/ids"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

// ErrNotFound — объекта нет.
var ErrNotFound = errors.New("storage: object not found")

// ObjectStorage — закрытая и публичная области хранилища.
type ObjectStorage interface {
	PutPrivate(ctx context.Context, key string, r io.Reader, size int64, contentType string) error
	// Get читает объект закрытой области; нет объекта — ErrNotFound. Вызывающий закрывает поток.
	Get(ctx context.Context, key string) (io.ReadCloser, int64, error)
	PresignGet(ctx context.Context, key string, ttl time.Duration) (string, error)
	// Delete удаляет объект закрытой области; отсутствующий объект — не ошибка.
	Delete(ctx context.Context, key string) error
	// CopyToPublic копирует объект закрытой области в публичную на стороне хранилища.
	CopyToPublic(ctx context.Context, privateKey, publicKey, contentType string) error
	// PublicURL — постоянная публичная ссылка на объект публичной области.
	PublicURL(publicKey string) string
	// NewPublicKey — ключ нового объекта публичной области (NewPublicKey).
	NewPublicKey(ext string) string
}

// NewKey генерирует имя объекта: `<area>/<uuidv7><ext>`. Имя от пользователя не используется (07 §4).
func NewKey(area, ext string) string {
	return area + "/" + ids.New().String() + ext
}

// S3Options — параметры подключения.
type S3Options struct {
	Endpoint string
	Region   string
	Bucket   string
	// PublicBucket — корзина публичной области; пустая — публичная область не настроена.
	PublicBucket string
	// PublicBaseURL — адрес, по которому читается публичная корзина; ключ дописывается в конец.
	PublicBaseURL string
	// PresignEndpoint — адрес хранилища в подписанных ссылках, если клиенты видят его иначе,
	// чем сервер (в разработке: localhost:9000 вместо minio:9000). Пусто — Endpoint.
	PresignEndpoint string
	AccessKey       string
	SecretKey       string
	UseSSL          bool
}

// S3 — реализация на minio-go.
type S3 struct {
	client *minio.Client
	// presigner подписывает ссылки; без PresignEndpoint — тот же client.
	presigner     *minio.Client
	bucket        string
	publicBucket  string
	publicBaseURL string
	clock         clock.Clock
	log           *slog.Logger
}

// NewS3 подключается к хранилищу и проверяет, что корзины есть.
func NewS3(ctx context.Context, o S3Options, clk clock.Clock, log *slog.Logger) (*S3, error) {
	client, err := minio.New(o.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(o.AccessKey, o.SecretKey, ""),
		Secure: o.UseSSL,
		Region: o.Region,
	})
	if err != nil {
		return nil, fmt.Errorf("storage client: %w", err)
	}
	buckets := []string{o.Bucket}
	if o.PublicBucket != "" {
		buckets = append(buckets, o.PublicBucket)
	}
	for _, b := range buckets {
		ok, err := client.BucketExists(ctx, b)
		if err != nil {
			return nil, fmt.Errorf("storage bucket check: %w", err)
		}
		if !ok {
			return nil, fmt.Errorf("storage bucket %q does not exist", b)
		}
	}
	presigner := client
	if o.PresignEndpoint != "" {
		// Подпись ссылки не обращается к хранилищу: регион задан, сеть не нужна.
		if presigner, err = minio.New(o.PresignEndpoint, &minio.Options{
			Creds:  credentials.NewStaticV4(o.AccessKey, o.SecretKey, ""),
			Secure: o.UseSSL,
			Region: o.Region,
		}); err != nil {
			return nil, fmt.Errorf("storage presign client: %w", err)
		}
	}
	log.DebugContext(ctx, "storage connected", logx.KeyOp, "Storage.Open", "endpoint", o.Endpoint,
		"presign_endpoint", o.PresignEndpoint, "bucket", o.Bucket, "public_bucket", o.PublicBucket)
	return &S3{client: client, presigner: presigner, bucket: o.Bucket, publicBucket: o.PublicBucket,
		publicBaseURL: strings.TrimRight(o.PublicBaseURL, "/"), clock: clk, log: log}, nil
}

// PutPrivate сохраняет объект в закрытой области.
func (s *S3) PutPrivate(ctx context.Context, key string, r io.Reader, size int64, contentType string) error {
	start := s.clock.Now()
	_, err := s.client.PutObject(ctx, s.bucket, key, r, size, minio.PutObjectOptions{ContentType: contentType})
	if err != nil {
		s.log.ErrorContext(ctx, "object storage failed", logx.KeyOp, "Storage.Put", "key", key, logx.KeyError, err.Error())
		return fmt.Errorf("put object: %w", err)
	}
	s.log.DebugContext(ctx, "object stored", logx.KeyOp, "Storage.Put", "key", key, "size", size,
		"duration_ms", s.clock.Now().Sub(start).Milliseconds())
	return nil
}

// Get читает объект закрытой области.
func (s *S3) Get(ctx context.Context, key string) (io.ReadCloser, int64, error) {
	obj, err := s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err == nil {
		var info minio.ObjectInfo
		if info, err = obj.Stat(); err == nil {
			s.log.DebugContext(ctx, "object read", logx.KeyOp, "Storage.Get", "key", key, "size", info.Size)
			return obj, info.Size, nil
		}
		_ = obj.Close()
	}
	if minio.ToErrorResponse(err).Code == minio.NoSuchKey {
		return nil, 0, ErrNotFound
	}
	s.log.ErrorContext(ctx, "object storage failed", logx.KeyOp, "Storage.Get", "key", key, logx.KeyError, err.Error())
	return nil, 0, fmt.Errorf("get object: %w", err)
}

// CopyToPublic копирует объект в публичную корзину с указанным типом содержимого.
func (s *S3) CopyToPublic(ctx context.Context, privateKey, publicKey, contentType string) error {
	if s.publicBucket == "" {
		return errors.New("storage: public bucket is not configured")
	}
	_, err := s.client.CopyObject(ctx,
		minio.CopyDestOptions{Bucket: s.publicBucket, Object: publicKey, ReplaceMetadata: true,
			UserMetadata: map[string]string{"Content-Type": contentType}},
		minio.CopySrcOptions{Bucket: s.bucket, Object: privateKey})
	if err != nil {
		if minio.ToErrorResponse(err).Code == minio.NoSuchKey {
			return ErrNotFound
		}
		s.log.ErrorContext(ctx, "object storage failed", logx.KeyOp, "Storage.CopyToPublic", "key", privateKey, logx.KeyError, err.Error())
		return fmt.Errorf("copy object to public: %w", err)
	}
	s.log.DebugContext(ctx, "object published", logx.KeyOp, "Storage.CopyToPublic", "key", privateKey, "public_key", publicKey)
	return nil
}

// PublicURL — адрес объекта публичной области.
func (s *S3) PublicURL(publicKey string) string { return s.publicBaseURL + "/" + publicKey }

// NewPublicKey — ключ нового публичного объекта.
func (s *S3) NewPublicKey(ext string) string { return NewPublicKey(ext) }

// PresignGet выдаёт подписанную ссылку на чтение со сроком ttl.
func (s *S3) PresignGet(ctx context.Context, key string, ttl time.Duration) (string, error) {
	u, err := s.presigner.PresignedGetObject(ctx, s.bucket, key, ttl, url.Values{})
	if err != nil {
		s.log.ErrorContext(ctx, "object storage failed", logx.KeyOp, "Storage.Presign", "key", key, logx.KeyError, err.Error())
		return "", fmt.Errorf("presign object: %w", err)
	}
	return u.String(), nil
}

// Delete удаляет объект.
func (s *S3) Delete(ctx context.Context, key string) error {
	if err := s.client.RemoveObject(ctx, s.bucket, key, minio.RemoveObjectOptions{}); err != nil {
		s.log.ErrorContext(ctx, "object storage failed", logx.KeyOp, "Storage.Delete", "key", key, logx.KeyError, err.Error())
		return fmt.Errorf("delete object: %w", err)
	}
	s.log.DebugContext(ctx, "object deleted", logx.KeyOp, "Storage.Delete", "key", key)
	return nil
}
