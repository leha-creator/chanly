package bridge

import (
	"context"

	postdomain "github.com/leha-creator/chanly/internal/post/domain"
)

// VersionListeners — несколько слушателей версии поста по порядку в одной транзакции
// (этап «Маркировка», R-11): сначала модуль размещений, затем маркировка. Ошибка любого
// откатывает переход версии.
type VersionListeners []postdomain.VersionListener

var _ postdomain.VersionListener = VersionListeners{}

func (ls VersionListeners) VersionChanged(ctx context.Context, change postdomain.VersionChange) error {
	for _, l := range ls {
		if l == nil {
			continue
		}
		if err := l.VersionChanged(ctx, change); err != nil {
			return err
		}
	}
	return nil
}
