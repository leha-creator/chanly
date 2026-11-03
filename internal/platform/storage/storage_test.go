package storage_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/leha-creator/chanly/internal/platform/clock"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/storage"
	"github.com/leha-creator/chanly/internal/platform/storage/storagefake"
)

// MinioImage — образ S3-совместимого хранилища для тестов (тот же, что в compose.dev.yml).
const MinioImage = "pgsty/minio:RELEASE.2026-08-04T00-00-00Z"

// contract проверяет поведение, общее для настоящего и поддельного хранилища.
// readPublic читает объект публичной области по его ссылке.
func contract(t *testing.T, s storage.ObjectStorage, read func(t *testing.T, key, presigned string) []byte,
	readPublic func(t *testing.T, key, url string) ([]byte, string)) {
	t.Helper()
	ctx := context.Background()
	key := storage.NewKey("test", ".pdf")
	if !strings.HasPrefix(key, "test/") || !strings.HasSuffix(key, ".pdf") {
		t.Fatalf("key %q", key)
	}
	data := []byte("%PDF-1.7 test document")
	if err := s.PutPrivate(ctx, key, bytes.NewReader(data), int64(len(data)), "application/pdf"); err != nil {
		t.Fatalf("put: %v", err)
	}
	u, err := s.PresignGet(ctx, key, 10*time.Minute)
	if err != nil || u == "" {
		t.Fatalf("presign: %q %v", u, err)
	}
	if got := read(t, key, u); !bytes.Equal(got, data) {
		t.Fatalf("read back %q", got)
	}

	// Get читает объект целиком и сообщает размер.
	rc, size, err := s.Get(ctx, key)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	got, _ := io.ReadAll(rc)
	_ = rc.Close()
	if size != int64(len(data)) || !bytes.Equal(got, data) {
		t.Fatalf("get: size %d, data %q", size, got)
	}
	if _, _, err := s.Get(ctx, "test/missing"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("get missing: want ErrNotFound, got %v", err)
	}

	// Копия в публичной области получает переданный тип и читается по постоянной ссылке.
	pub := storage.NewPublicKey(".jpg")
	if err := s.CopyToPublic(ctx, key, pub, "image/jpeg"); err != nil {
		t.Fatalf("copy to public: %v", err)
	}
	pubData, contentType := readPublic(t, pub, s.PublicURL(pub))
	if !bytes.Equal(pubData, data) || contentType != "image/jpeg" {
		t.Fatalf("public copy: %q, content type %q", pubData, contentType)
	}
	if err := s.CopyToPublic(ctx, "test/missing", storage.NewPublicKey(".jpg"), "image/jpeg"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("copy missing: want ErrNotFound, got %v", err)
	}

	if err := s.Delete(ctx, key); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := s.Delete(ctx, key); err != nil {
		t.Fatalf("delete missing object: %v", err)
	}
}

func TestNewPublicKey(t *testing.T) {
	re := regexp.MustCompile(`^pub/[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[0-9a-f]{4}-[0-9a-f]{12}-[0-9a-f]{32}\.png$`)
	seen := map[string]bool{}
	for range 100 {
		k := storage.NewPublicKey(".png")
		if !re.MatchString(k) {
			t.Fatalf("key %q has unexpected form", k)
		}
		if seen[k] {
			t.Fatalf("key %q repeated", k)
		}
		seen[k] = true
	}
}

func TestFakeContract(t *testing.T) {
	s := storagefake.New()
	contract(t, s, func(t *testing.T, key, presigned string) []byte {
		if !strings.Contains(presigned, "expires=600") {
			t.Fatalf("presigned %q", presigned)
		}
		o, ok := s.Object(key)
		if !ok || o.ContentType != "application/pdf" {
			t.Fatalf("object %v %v", o, ok)
		}
		return o.Data
	}, func(t *testing.T, key, url string) ([]byte, string) {
		if url != storagefake.PublicBaseURL+"/"+key {
			t.Fatalf("public url %q", url)
		}
		o, ok := s.Public(key)
		if !ok {
			t.Fatalf("public object %q missing", key)
		}
		return o.Data, o.ContentType
	})
	if s.Len() != 0 {
		t.Fatal("object not deleted")
	}
	if s.Copies() != 1 {
		t.Fatalf("copies = %d, want 1", s.Copies())
	}
}

func TestFakeFailWith(t *testing.T) {
	s := storagefake.New()
	ctx := context.Background()
	boom := errors.New("boom")
	s.FailWith(storagefake.OpPut, boom)
	if err := s.PutPrivate(ctx, "k", strings.NewReader("x"), 1, "text/plain"); !errors.Is(err, boom) {
		t.Fatalf("put with failure: %v", err)
	}
	s.FailWith(storagefake.OpPut, nil)
	if err := s.PutPrivate(ctx, "k", strings.NewReader("x"), 1, "text/plain"); err != nil {
		t.Fatalf("put after failure removed: %v", err)
	}
	s.FailWith(storagefake.OpGet, boom)
	if _, _, err := s.Get(ctx, "k"); !errors.Is(err, boom) {
		t.Fatalf("get with failure: %v", err)
	}
}

func TestMinioContract(t *testing.T) {
	if testing.Short() {
		t.Skip("MinIO container test is skipped with -short")
	}
	ctx := context.Background()
	ctr, err := testcontainers.Run(ctx, MinioImage,
		testcontainers.WithCmd("server", "/data"),
		testcontainers.WithEnv(map[string]string{"MINIO_ROOT_USER": "minio", "MINIO_ROOT_PASSWORD": "minio-secret"}),
		testcontainers.WithExposedPorts("9000/tcp"),
		testcontainers.WithWaitStrategy(wait.ForHTTP("/minio/health/live").WithPort("9000/tcp").WithStartupTimeout(time.Minute)),
	)
	if err != nil {
		if os.Getenv("CI") == "true" {
			t.Fatalf("MinIO is required in CI: %v", err)
		}
		t.Skipf("Docker is not available, MinIO test is skipped: %v", err)
	}
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(ctr) })
	endpoint, err := ctr.PortEndpoint(ctx, "9000/tcp", "")
	if err != nil {
		t.Fatal(err)
	}
	admin, err := minio.New(endpoint, &minio.Options{Creds: credentials.NewStaticV4("minio", "minio-secret", "")})
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range []string{"private", "public"} {
		if err := admin.MakeBucket(ctx, b, minio.MakeBucketOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	// Публичная корзина: анонимное чтение объектов без перечисления (как minio-init в compose.dev.yml).
	if err := admin.SetBucketPolicy(ctx, "public", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow",
		"Principal":{"AWS":["*"]},"Action":["s3:GetObject"],"Resource":["arn:aws:s3:::public/*"]}]}`); err != nil {
		t.Fatal(err)
	}

	opts := storage.S3Options{Endpoint: endpoint, Region: "us-east-1", Bucket: "private", PublicBucket: "public",
		PublicBaseURL: "http://" + endpoint + "/public/", AccessKey: "minio", SecretKey: "minio-secret"}
	for _, bad := range []storage.S3Options{
		{Endpoint: endpoint, Region: "us-east-1", Bucket: "missing", AccessKey: "minio", SecretKey: "minio-secret"},
		{Endpoint: endpoint, Region: "us-east-1", Bucket: "private", PublicBucket: "missing", AccessKey: "minio", SecretKey: "minio-secret"},
	} {
		if _, err := storage.NewS3(ctx, bad, clock.System{}, logx.Discard()); err == nil {
			t.Fatalf("missing bucket accepted: %+v", bad)
		}
	}
	s, err := storage.NewS3(ctx, opts, clock.System{}, logx.Discard())
	if err != nil {
		t.Fatal(err)
	}
	get := func(t *testing.T, url string) (*http.Response, []byte) {
		t.Helper()
		resp, err := http.Get(url)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp, b
	}
	contract(t, s, func(t *testing.T, key, presigned string) []byte {
		resp, b := get(t, presigned)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("presigned GET: %d", resp.StatusCode)
		}
		return b
	}, func(t *testing.T, key, url string) ([]byte, string) {
		if url != "http://"+endpoint+"/public/"+key {
			t.Fatalf("public url %q", url)
		}
		resp, b := get(t, url)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("anonymous public GET: %d", resp.StatusCode)
		}
		return b, resp.Header.Get("Content-Type")
	})
	// Подписанная ссылка на другом адресе того же хранилища (как localhost:9000 вместо minio:9000).
	host, port, _ := strings.Cut(endpoint, ":")
	alt := "127.0.0.1:" + port
	if host == "127.0.0.1" {
		alt = "localhost:" + port
	}
	popts := opts
	popts.PresignEndpoint = alt
	ps, err := storage.NewS3(ctx, popts, clock.System{}, logx.Discard())
	if err != nil {
		t.Fatal(err)
	}
	key := storage.NewKey("test", ".txt")
	if err := ps.PutPrivate(ctx, key, strings.NewReader("presigned"), 9, "text/plain"); err != nil {
		t.Fatal(err)
	}
	u, err := ps.PresignGet(ctx, key, time.Minute)
	if err != nil || !strings.HasPrefix(u, "http://"+alt+"/") {
		t.Fatalf("presign endpoint: %q %v", u, err)
	}
	if resp, b := get(t, u); resp.StatusCode != http.StatusOK || string(b) != "presigned" {
		t.Fatalf("presigned GET via %s: %d %s", alt, resp.StatusCode, b)
	}

	// Закрытая область без подписи недоступна, перечисление публичной закрыто.
	for _, u := range []string{"http://" + endpoint + "/private/test/anything", "http://" + endpoint + "/public/"} {
		if resp, _ := get(t, u); resp.StatusCode == http.StatusOK {
			t.Fatalf("%s is readable without signature", u)
		}
	}
}
