package db_test

import (
	"context"
	"testing"

	"github.com/leha-creator/chanly/internal/platform/db"
	"github.com/leha-creator/chanly/internal/platform/dbtest"
	"github.com/leha-creator/chanly/internal/platform/ids"
)

// mediaPostFixture записывает организацию, пользователя, файл, готовую производную, кампанию и пост.
func mediaPostFixture(t *testing.T, d *dbtest.DB) (org, user, file, derivative, post string) {
	t.Helper()
	ctx := context.Background()
	orgID, userID, fileID, derivID, campaignID, postID := ids.New(), ids.New(), ids.New(), ids.New(), ids.New(), ids.New()
	steps := []struct {
		stmt string
		args []any
	}{
		{`INSERT INTO organization (id, legal_type, inn, name, contact_name, phone, status, created_at, updated_at)
			VALUES ($1, 'legal_entity', '7707083893', 'Рекламодатель', 'Рекламодатель', '+79990000000', 'active', now(), now())`, []any{orgID}},
		{`INSERT INTO "user" (id, email, status, created_at, updated_at) VALUES ($1, $2, 'active', now(), now())`,
			[]any{userID, userID.String() + "@example.test"}},
		{`INSERT INTO media_file (id, organization_id, storage_key, mime, size_bytes, width, height, sha256, original_name, created_by, created_at)
			VALUES ($1, $2, $3, 'image/jpeg', 1000, 800, 600, 'abc', 'a.jpg', $4, now())`, []any{fileID, orgID, "media/" + fileID.String(), userID}},
		{`INSERT INTO media_derivative (id, media_file_id, kind, params_hash, status, storage_key, mime, width, height, size_bytes, created_at)
			VALUES ($1, $2, 'plain', 'h1', 'ready', $3, 'image/jpeg', 800, 600, 900, now())`, []any{derivID, fileID, "derivative/" + derivID.String()}},
		{`INSERT INTO campaign (id, organization_id, name, status, created_by, created_at, updated_at)
			VALUES ($1, $2, 'Кампания', 'draft', $3, now(), now())`, []any{campaignID, orgID, userID}},
		{`INSERT INTO post (id, organization_id, campaign_id, status, draft, created_at, updated_at)
			VALUES ($1, $2, $3, 'draft', '{}', now(), now())`, []any{postID, orgID, campaignID}},
	}
	for _, s := range steps {
		if _, err := d.Pool.Exec(ctx, s.stmt, s.args...); err != nil {
			t.Fatalf("fixture: %v", err)
		}
	}
	return orgID.String(), userID.String(), fileID.String(), derivID.String(), postID.String()
}

func TestMediaPostTablesForAppRole(t *testing.T) {
	d := dbtest.New(t)
	ctx := context.Background()
	org, user, file, deriv, post := mediaPostFixture(t, d)

	// R-2: второй живой файл с тем же sha256 в организации не вставляется, после удаления — вставляется.
	dup := `INSERT INTO media_file (id, organization_id, storage_key, mime, size_bytes, width, height, sha256, original_name, created_by, created_at)
		VALUES ($1, $2, $3, 'image/png', 10, 10, 10, 'abc', 'b.png', $4, now())`
	second := ids.New()
	if _, err := d.Pool.Exec(ctx, dup, second, org, "media/"+second.String(), user); !db.UniqueViolation(err, "media_file_org_sha256_key") {
		t.Fatalf("duplicate sha256: want unique violation, got %v", err)
	}
	if _, err := d.Pool.Exec(ctx, `UPDATE media_file SET deleted_at = now() WHERE id = $1`, file); err != nil {
		t.Fatalf("app role cannot soft-delete media_file: %v", err)
	}
	if _, err := d.Pool.Exec(ctx, dup, second, org, "media/"+second.String(), user); err != nil {
		t.Fatalf("same sha256 after soft delete: %v", err)
	}
	if _, err := d.Pool.Exec(ctx, `INSERT INTO media_file (id, organization_id, storage_key, mime, size_bytes, width, height, sha256, original_name, created_by, created_at)
		VALUES ($1, $2, 'k-big', 'image/jpeg', 52428801, 10, 10, 'big', 'c.jpg', $3, now())`, ids.New(), org, user); err == nil {
		t.Error("file larger than 50 MB accepted")
	}

	// Производная: тройка уникальна, ready требует ключа и размеров.
	if _, err := d.Pool.Exec(ctx, `INSERT INTO media_derivative (id, media_file_id, kind, params_hash, status, created_at)
		VALUES ($1, $2, 'plain', 'h1', 'pending', now())`, ids.New(), file); !db.UniqueViolation(err, "media_derivative_triple_key") {
		t.Fatalf("duplicate derivative triple: want unique violation, got %v", err)
	}
	if _, err := d.Pool.Exec(ctx, `INSERT INTO media_derivative (id, media_file_id, kind, params_hash, status, created_at)
		VALUES ($1, $2, 'labeled', 'h2', 'ready', now())`, ids.New(), file); !db.CheckViolation(err, "media_derivative_ready_fields") {
		t.Fatalf("ready derivative without fields: want check violation, got %v", err)
	}

	// Версии: номер уникален в посте; ready неизменяема вместе с изображениями (R-18, I-09).
	version := ids.New()
	insertVersion := `INSERT INTO post_version (id, post_id, number, status, content_doc, body, token_carrier, token_position,
		advertiser_name, advertiser_inn, created_by, created_at)
		VALUES ($1, $2, $3, 'approved', '{}', 'текст', 'text', 'below', 'ООО Ромашка', '7707083893', $4, now())`
	if _, err := d.Pool.Exec(ctx, insertVersion, version, post, 1, user); err != nil {
		t.Fatalf("app role cannot insert post_version: %v", err)
	}
	if _, err := d.Pool.Exec(ctx, insertVersion, ids.New(), post, 1, user); !db.UniqueViolation(err, "post_version_number_key") {
		t.Fatalf("duplicate version number: want unique violation, got %v", err)
	}
	if _, err := d.Pool.Exec(ctx, `INSERT INTO post_version_media (id, post_version_id, position, media_file_id, media_derivative_id, created_at)
		VALUES ($1, $2, 0, $3, $4, now())`, ids.New(), version, file, deriv); err != nil {
		t.Fatalf("app role cannot insert post_version_media: %v", err)
	}
	if _, err := d.Pool.Exec(ctx, `UPDATE post_version SET status = 'ready' WHERE id = $1`, version); !db.CheckViolation(err, "post_version_ready_fields") {
		t.Fatalf("ready without final_text: want check violation, got %v", err)
	}
	if _, err := d.Pool.Exec(ctx, `UPDATE post_version SET status = 'ready', final_text = 'текст', erid = 'E1', lock_version = lock_version + 1
		WHERE id = $1`, version); err != nil {
		t.Fatalf("transition to ready: %v", err)
	}
	for _, stmt := range []string{
		`UPDATE post_version SET body = 'другой' WHERE id = $1`,
		`DELETE FROM post_version WHERE id = $1`,
		`UPDATE post_version_media SET position = 1 WHERE post_version_id = $1`,
		`DELETE FROM post_version_media WHERE post_version_id = $1`,
	} {
		if _, err := d.Pool.Exec(ctx, stmt, version); !db.CheckViolation(err, "post_version_ready_immutable") {
			t.Errorf("%s: want post_version_ready_immutable, got %v", stmt, err)
		}
	}
	if _, err := d.Pool.Exec(ctx, `INSERT INTO post_version_media (id, post_version_id, position, media_file_id, media_derivative_id, created_at)
		VALUES ($1, $2, 1, $3, $4, now())`, ids.New(), version, file, deriv); !db.CheckViolation(err, "post_version_ready_immutable") {
		t.Errorf("insert media into ready version: want post_version_ready_immutable, got %v", err)
	}

	// Кампания: период и окно времени — по порядку, время — парой, название не пустое.
	for _, stmt := range []string{
		`INSERT INTO campaign (id, organization_id, name, status, date_from, date_to, created_by, created_at, updated_at)
			VALUES ($1, $2, 'К', 'draft', '2026-10-20', '2026-10-19', $3, now(), now())`,
		`INSERT INTO campaign (id, organization_id, name, status, time_from, created_by, created_at, updated_at)
			VALUES ($1, $2, 'К', 'draft', '10:00', $3, now(), now())`,
		`INSERT INTO campaign (id, organization_id, name, status, created_by, created_at, updated_at)
			VALUES ($1, $2, '', 'draft', $3, now(), now())`,
	} {
		if _, err := d.Pool.Exec(ctx, stmt, ids.New(), org, user); err == nil {
			t.Errorf("invalid campaign accepted: %s", stmt)
		}
	}
}
