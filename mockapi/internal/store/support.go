package store

import (
	"bytes"
	_ "embed"
	"github.com/google/uuid"
	"image"
	"image/color"
	"image/png"
	"maxim/mockapi/internal/api"
	"time"
)

type SupportState struct {
	Conversations map[uuid.UUID]api.SupportConversation
	Messages      map[uuid.UUID][]api.SupportMessage
	Attachments   map[uuid.UUID]api.SupportAttachment
	Files         map[uuid.UUID][]byte
	Owners        map[uuid.UUID]uuid.UUID
	Keys          map[uuid.UUID]map[uuid.UUID]SupportKey
	Reads         map[uuid.UUID]map[uuid.UUID]int64
}
type SupportKey struct {
	Request string
	Message api.SupportMessage
}

//go:embed testdata/support-audio.mp3
var supportAudio []byte
//go:embed testdata/support-video.mp4
var supportVideo []byte

func NewSupport(now time.Time) *SupportState {
	s := &SupportState{Conversations: map[uuid.UUID]api.SupportConversation{}, Messages: map[uuid.UUID][]api.SupportMessage{}, Attachments: map[uuid.UUID]api.SupportAttachment{}, Files: map[uuid.UUID][]byte{}, Owners: map[uuid.UUID]uuid.UUID{}, Keys: map[uuid.UUID]map[uuid.UUID]SupportKey{}, Reads: map[uuid.UUID]map[uuid.UUID]int64{}}
	names := []string{"Мария — вопрос по каналу", "Алексей — оплата счёта", "Ольга — размещение"}
	texts := []string{"Здравствуйте! Не получается подключить канал. Прикрепляю скриншот.", "Подскажите, пожалуйста, когда поступит оплата?", "Спасибо, всё получилось!"}
	for i, name := range names {
		id := uuid.MustParse([]string{"019a9000-0000-7000-8000-000000001201", "019a9000-0000-7000-8000-000000001202", "019a9000-0000-7000-8000-000000001203"}[i])
		c := api.SupportConversation{Id: id, BotId: "100", MaxUserId: []string{"9007199254740999", "102", "103"}[i], Name: name, Username: "demo_user", Status: api.SupportConversationStatus("open"), Version: 1, LastSequence: 1, LastIncoming: 1, NeedsReply: true, Unread: 1, Preview: texts[i], CreatedAt: now.Add(-time.Duration(i+1) * time.Hour), UpdatedAt: now.Add(-time.Duration(i) * time.Minute)}
		if i == 2 {
			c.Status = "closed"
			c.AnsweredThrough = 1
			c.NeedsReply = false
		}
		s.Conversations[id] = c
		s.Messages[id] = []api.SupportMessage{{Id: NewID(), ConversationId: id, Sequence: 1, Direction: "inbound", Text: texts[i], Status: "sent", CreatedAt: c.CreatedAt, Attachments: []api.SupportAttachment{}}}
	}
	var b bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 320, 180))
	for y := 0; y < 180; y++ {
		for x := 0; x < 320; x++ {
			img.Set(x, y, color.RGBA{235, 241, 250, 255})
		}
	}
	_ = png.Encode(&b, img)
	cid := uuid.MustParse("019a9000-0000-7000-8000-000000001201")
	a := api.SupportAttachment{Id: NewID(), Type: "image", Mime: "image/png", Name: "Скриншот.png", Size: int64(b.Len()), Status: "ready", Width: 320, Height: 180}
	s.Attachments[a.Id] = a
	s.Files[a.Id] = b.Bytes()
	s.Messages[cid][0].Attachments = []api.SupportAttachment{a}
	for i, status := range []api.SupportMessageStatus{"queued", "failed", "unknown"} {
		m := api.SupportMessage{Id: NewID(), ConversationId: cid, Sequence: int64(i + 2), Direction: "outbound", Text: "Проверяем подключение канала.", Status: status, Reason: map[api.SupportMessageStatus]string{"failed": "rejected_403", "unknown": "outcome_unknown"}[status], CreatedAt: now, Attachments: []api.SupportAttachment{}}
		s.Messages[cid] = append(s.Messages[cid], m)
	}
	c := s.Conversations[cid]
	c.LastSequence = 4
	s.Conversations[cid] = c
	mediaID:=uuid.MustParse("019a9000-0000-7000-8000-000000001202")
	for _,fixture:=range []struct{kind,mime,name string;data []byte}{{"audio","audio/mpeg","Голосовое.mp3",supportAudio},{"video","video/mp4","Видео.mp4",supportVideo}}{a:=api.SupportAttachment{Id:NewID(),Type:api.SupportAttachmentType(fixture.kind),Mime:fixture.mime,Name:fixture.name,Size:int64(len(fixture.data)),Status:"ready",Duration:2};s.Attachments[a.Id]=a;s.Files[a.Id]=fixture.data;seq:=int64(len(s.Messages[mediaID])+1);s.Messages[mediaID]=append(s.Messages[mediaID],api.SupportMessage{Id:NewID(),ConversationId:mediaID,Sequence:seq,Direction:"inbound",Status:"sent",CreatedAt:now,Attachments:[]api.SupportAttachment{a}});c:=s.Conversations[mediaID];c.LastSequence=seq;c.LastIncoming=seq;s.Conversations[mediaID]=c}
	return s
}
func (s *SupportState) ForActor(id, actor uuid.UUID) api.SupportConversation {
	c := s.Conversations[id]
	marker := int64(0)
	if s.Reads[id] != nil {
		marker = s.Reads[id][actor]
	}
	c.Unread = 0
	for _, m := range s.Messages[id] {
		if m.Direction == "inbound" && m.Sequence > marker {
			c.Unread++
		}
	}
	return c
}
func (s *SupportState) Waiting() int {
	n := 0
	for _, c := range s.Conversations {
		if c.Status == "open" && c.LastIncoming > c.AnsweredThrough {
			n++
		}
	}
	return n
}
