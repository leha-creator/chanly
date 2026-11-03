package bootstrap

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"text/tabwriter"
	"time"

	"github.com/leha-creator/chanly/internal/account/domain"
	"github.com/leha-creator/chanly/internal/platform/db"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

// runLegal — служебная команда оферт до появления API администратора (R-8):
//
//	maxim legal publish --kind <kind> --file <path.pdf> [--published-at <RFC3339>]
//	maxim legal list
func runLegal(ctx context.Context, cfg Config, log *slog.Logger, args []string, out io.Writer) error {
	if len(args) == 0 {
		return errUsage
	}
	pool, err := db.Open(ctx, cfg.DatabaseURL, 2, log)
	if err != nil {
		return err
	}
	defer pool.Close()
	in, err := newInfra(ctx, cfg, ModeLegal, pool, log, nil)
	if err != nil {
		return err
	}
	m := newModules(cfg, in)

	switch args[0] {
	case "publish":
		fs := flag.NewFlagSet("legal publish", flag.ContinueOnError)
		fs.SetOutput(out)
		kind := fs.String("kind", "", "advertiser_offer | blogger_offer | platform_rules | privacy_policy")
		file := fs.String("file", "", "path to the PDF file")
		publishedAt := fs.String("published-at", "", "publication time, RFC 3339 (default: now)")
		if err := fs.Parse(args[1:]); err != nil || *kind == "" || *file == "" {
			return errUsage
		}
		at := in.clock.Now()
		if *publishedAt != "" {
			if at, err = time.Parse(time.RFC3339, *publishedAt); err != nil {
				return errUsage
			}
		}
		data, err := os.ReadFile(*file)
		if err != nil {
			return fmt.Errorf("read %s: %w", *file, err)
		}
		d, err := m.account.PublishLegalDocument(ctx, domain.LegalDocumentKind(*kind), data, at.UTC())
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "published %s version %d id %s sha256 %s\n", d.Kind, d.Version, d.ID, d.SHA256)
		return nil
	case "list":
		docs, err := m.account.CurrentLegalDocuments(ctx)
		if err != nil {
			return err
		}
		tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "KIND\tVERSION\tPUBLISHED_AT\tID\tSHA256")
		for _, d := range docs {
			fmt.Fprintf(tw, "%s\t%d\t%s\t%s\t%s\n", d.Kind, d.Version, d.PublishedAt.Format(time.RFC3339), d.ID, d.SHA256)
		}
		return tw.Flush()
	default:
		log.ErrorContext(ctx, "unknown legal command", logx.KeyOp, "Bootstrap.Legal", "command", args[0])
		return errUsage
	}
}
