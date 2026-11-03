package app_test

import (
	"io"
	"testing"

	"github.com/leha-creator/chanly/internal/platform/job"
	"github.com/leha-creator/chanly/internal/post/domain"
)

func TestPublicationContent(t *testing.T) {
	e := newEnv(t)
	c := e.readyDraft(domain.CarrierMedia)
	v, err := e.svc.CreateVersion(e.ctx(), e.org, c.ID, e.user, 42)
	if err != nil {
		t.Fatal(err)
	}
	e.drain()

	// До ready маркировка не проверена.
	pc, err := e.svc.PublicationContent(e.ctx(), v.ID)
	if err != nil || pc.MarkingOK || len(pc.Media) != 1 {
		t.Fatalf("pending: %+v %v", pc, err)
	}
	if _, err := e.svc.ApproveVersion(e.ctx(), v.ID, e.user); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.MarkVersionReady(e.ctx(), v.ID, "2VtzqvAbc"); err != nil {
		t.Fatal(err)
	}
	pc, err = e.svc.PublicationContent(e.ctx(), v.ID)
	if err != nil || !pc.MarkingOK || pc.Version.Erid != "2VtzqvAbc" || len(pc.Media) != 1 || pc.Media[0].Size == 0 ||
		pc.Media[0].ContentType == "" || pc.Media[0].DerivativeID != v.Media[0].DerivativeID {
		t.Fatalf("ready: %+v %v", pc, err)
	}

	// Файл читается из закрытой области; размер совпадает с производной.
	r, err := e.svc.OpenDerivative(e.ctx(), pc.Media[0].DerivativeID)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(r)
	_ = r.Close()
	if int64(len(data)) != pc.Media[0].Size {
		t.Fatalf("read %d bytes, derivative %d", len(data), pc.Media[0].Size)
	}
	if _, err := e.svc.OpenDerivative(e.ctx(), v.ID); !job.IsPermanent(err) {
		t.Fatalf("unknown derivative: %v", err)
	}
}
