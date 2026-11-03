package storage

import (
	"crypto/rand"
	"encoding/hex"

	"github.com/leha-creator/chanly/internal/platform/ids"
)

// PublicArea — префикс ключей публичной области.
const PublicArea = "pub"

// NewPublicKey генерирует ключ публичного объекта: `pub/<uuidv7>-<32 hex>.<ext>` (R-7).
// Случайная часть не даёт угадать соседние ключи по времени создания UUID v7;
// перечисление объектов публичной корзины закрыто.
func NewPublicKey(ext string) string {
	var b [16]byte
	_, _ = rand.Read(b[:]) // crypto/rand.Read не возвращает ошибку (Go 1.24+)
	return PublicArea + "/" + ids.New().String() + "-" + hex.EncodeToString(b[:]) + ext
}
