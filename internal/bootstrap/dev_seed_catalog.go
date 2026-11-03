package bootstrap

import (
	"context"
	"fmt"
	"image/color"

	"github.com/google/uuid"

	channelapp "github.com/leha-creator/chanly/internal/channel/app"
	channeldomain "github.com/leha-creator/chanly/internal/channel/domain"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/money"
	postapp "github.com/leha-creator/chanly/internal/post/app"
	postdomain "github.com/leha-creator/chanly/internal/post/domain"
)

// seedChatBase — зарезервированный диапазон max_chat_id каналов dev seed: -7 700 000 000 - n (R-2).
const seedChatBase int64 = -7_700_000_000

// seedChannelSpec — канал каталога dev seed.
type seedChannelSpec struct {
	n           int64
	title       string
	description string
	subscribers int
	// ul — канал блогера-юрлица; иначе самозанятого.
	ul bool
	// moderation — канал остаётся в очереди модерации.
	moderation bool
}

// seedChannelSpecs — 8 каналов каталога и 1 в очереди; подписчиков меньше порога РКН.
var seedChannelSpecs = []seedChannelSpec{
	{n: 1, title: "Кухня без суеты", description: "Простые рецепты на каждый день", subscribers: 4200},
	{n: 2, title: "Городской гид", description: "Куда сходить в выходные", subscribers: 9500},
	{n: 3, title: "Гаджеты и софт", description: "Обзоры техники и приложений", subscribers: 7300},
	{n: 4, title: "Путешествия налегке", description: "Маршруты и лайфхаки для поездок", subscribers: 3100},
	{n: 5, title: "Финансы без воды", description: "Личный бюджет и налоги простым языком", subscribers: 5600},
	{n: 6, title: "Спорт дома", description: "Тренировки без зала", subscribers: 1200},
	{n: 7, title: "Книжная полка", description: "Новинки и подборки книг", subscribers: 2800, ul: true},
	{n: 8, title: "Дизайн интерьера", description: "Идеи для квартиры и дачи", subscribers: 6400, ul: true},
	{n: 9, title: "Новости района", description: "Что происходит рядом с домом", subscribers: 1900, moderation: true},
}

// seedPrices — цены трёх форматов: от 1 500 ₽ за 1/24 плюс 1 ₽ за подписчика.
func seedPrices(subscribers int) []channeldomain.Price {
	base := money.Kopecks(1_500_00 + subscribers*100)
	return []channeldomain.Price{
		{FormatID: "f_1_24", Amount: base, IsEnabled: true},
		{FormatID: "f_2_48", Amount: base * 16 / 10, IsEnabled: true},
		{FormatID: "f_3_72", Amount: base * 22 / 10, IsEnabled: true},
	}
}

// seedChannels создаёт каналы, заполняет их и одобряет; ждёт регистрации площадок в ОРД.
func (s *seeder) seedChannels(ctx context.Context) error {
	const op = "Bootstrap.DevSeed"
	cats, err := s.m.channel.Categories.ListActive(ctx)
	if err != nil {
		return err
	}
	if len(cats) == 0 {
		return fmt.Errorf("no active channel categories")
	}
	var pending []int64
	for i, spec := range seedChannelSpecs {
		owner := &s.blg
		if spec.ul {
			owner = &s.blgUL
		}
		c, created, err := s.m.channel.DevSeedChannel(ctx, owner.orgID, channelapp.DevChannelInput{
			MaxChatID: seedChatBase - spec.n, Title: spec.title, Description: spec.description,
			Link: fmt.Sprintf("https://max.ru/demo_channel_%d", spec.n), Subscribers: spec.subscribers,
		})
		if err != nil {
			return fmt.Errorf("channel %q: %w", spec.title, err)
		}
		if c.Status == channeldomain.StatusModeration {
			cat := cats[i%len(cats)].ID
			if _, err := s.m.channel.Update(ctx, owner.orgID, c.ID, channeldomain.ProfileUpdate{
				Description: &spec.description, CategoryID: &cat}); err != nil {
				return fmt.Errorf("channel %q: %w", spec.title, err)
			}
			if _, err := s.m.channel.SetPrices(ctx, owner.orgID, c.ID, seedPrices(spec.subscribers)); err != nil {
				return fmt.Errorf("channel %q: %w", spec.title, err)
			}
			if !spec.moderation {
				if _, err := s.m.channel.Approve(ctx, s.moderator, c.ID); err != nil {
					return fmt.Errorf("channel %q: %w", spec.title, err)
				}
			}
		}
		if created {
			state := "каталог"
			if spec.moderation {
				state = "модерация"
			}
			s.log.InfoContext(ctx, "seed channel", logx.KeyOp, op, channelapp.KeyChannelID, c.ID, "max_chat_id", c.MaxChatID,
				"moderation", spec.moderation)
			s.note("канал «%s» (%s)", spec.title, state)
		}
		if spec.moderation {
			continue
		}
		s.channels = append(s.channels, seedChannel{id: c.ID, title: spec.title, owner: owner})
		pending = append(pending, c.MaxChatID)
	}
	return s.wait.Wait(ctx, "register channel platforms in ORD", func(ctx context.Context) (bool, error) {
		rest := pending[:0]
		for _, chatID := range pending {
			c, err := s.m.channel.Channels.GetByMaxChatID(ctx, chatID)
			if err != nil {
				return false, err
			}
			if c.OrdPlatformReadyAt == nil {
				rest = append(rest, chatID)
			}
		}
		pending = rest
		return len(pending) == 0, nil
	})
}

// seedCampaigns — кампании рекламодателя без заказа: с готовым постом и пустая.
func (s *seeder) seedCampaigns(ctx context.Context) error {
	existing, err := s.campaignNames(ctx)
	if err != nil {
		return err
	}
	if !existing["Демо: черновик"] {
		media, err := s.m.post.Upload(ctx, s.adv.orgID, s.adv.userID, "demo.png", seedPNG(color.RGBA{R: 64, G: 112, B: 214, A: 255}))
		if err != nil {
			return err
		}
		if _, err := s.campaign(ctx, "Демо: черновик", "Весенняя распродажа: скидки до 30% на всё. Подробности — https://shop.example.test/sale",
			media.File.ID); err != nil {
			return err
		}
	}
	if !existing["Демо: пустая"] {
		name := "Демо: пустая"
		c, err := s.m.post.CreateCampaign(ctx, s.adv.orgID, s.adv.userID, postdomain.CampaignInput{Name: &name})
		if err != nil {
			return err
		}
		s.note("кампания «%s»", c.Name)
	}
	return nil
}

// campaignNames — названия кампаний рекламодателя: по ним повтор находит созданное.
func (s *seeder) campaignNames(ctx context.Context) (map[string]bool, error) {
	names := map[string]bool{}
	cursor := ""
	for {
		page, err := s.m.post.ListCampaigns(ctx, s.adv.orgID, nil, cursor, 100)
		if err != nil {
			return nil, err
		}
		for _, c := range page.Items {
			names[c.Name] = true
		}
		if page.NextCursor == "" {
			return names, nil
		}
		cursor = page.NextCursor
	}
}

// campaign — кампания на окно сегодня … +2 дня 00:00–23:59 МСК с постом из текста и медиа.
func (s *seeder) campaign(ctx context.Context, name, text string, media ...uuid.UUID) (postdomain.Campaign, error) {
	from, to := seedCampaignWindow(s.m.in.clock.Now())
	tf, tt := postdomain.TimeOfDay(0), postdomain.TimeOfDay(23*60+59)
	c, err := s.m.post.CreateCampaign(ctx, s.adv.orgID, s.adv.userID, postdomain.CampaignInput{
		Name: &name, DateFrom: &from, DateTo: &to, TimeFrom: &tf, TimeTo: &tt,
	})
	if err != nil {
		return postdomain.Campaign{}, fmt.Errorf("campaign %q: %w", name, err)
	}
	doc := postdomain.Doc{Blocks: []postdomain.Block{{Type: postdomain.BlockParagraph, Content: []postdomain.Text{{Text: text}}}}}
	if media == nil {
		media = []uuid.UUID{}
	}
	if _, err := s.m.post.SaveDraft(ctx, s.adv.orgID, s.adv.userID, c.ID, postapp.DraftInput{Doc: doc, MediaIDs: media}); err != nil {
		return postdomain.Campaign{}, fmt.Errorf("campaign %q post: %w", name, err)
	}
	s.log.InfoContext(ctx, "seed campaign", logx.KeyOp, "Bootstrap.DevSeed", "campaign_id", c.ID, "name", name)
	s.note("кампания «%s»", name)
	return c, nil
}
