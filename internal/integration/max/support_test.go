package max

import (
	"bytes"
	"errors"
	"github.com/leha-creator/chanly/internal/platform/outbound"
	"io"
	"mime/multipart"
	"testing"
)

func TestSupportUploadPartUsesMediaTypeAndUTF8Filename(t *testing.T) {
	data := append([]byte("ID3"), bytes.Repeat([]byte{0}, 600)...)
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, source, err := supportUploadPart(writer, "Голосовое сообщение.mp3", bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = io.Copy(part, source); err != nil {
		t.Fatal(err)
	}
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	upload := multipart.NewReader(&body, writer.Boundary())
	file, err := upload.NextPart()
	if err != nil {
		t.Fatal(err)
	}
	if file.FormName() != "data" || file.FileName() != "Голосовое сообщение.mp3" || file.Header.Get("Content-Type") != "audio/mpeg" {
		t.Fatal("voice multipart metadata is incompatible with the CDN")
	}
	got, err := io.ReadAll(file)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatal("media sniffing consumed upload bytes")
	}
}

func TestSupportOutcome(t *testing.T) {
	for _, tt := range []struct {
		status    int
		body      string
		def, temp bool
	}{{403, "", true, false}, {429, "", true, true}, {400, `{"code":"attachment.not.ready"}`, true, true}, {500, "", false, false}, {0, "", false, false}} {
		err := supportOutcome(outbound.Response{Status: tt.status, Body: []byte(tt.body)}, errors.New("private provider body"))
		var e *SupportDeliveryError
		if !errors.As(err, &e) || e.Definitive != tt.def || e.Temporary != tt.temp {
			t.Fatalf("status=%d: %v", tt.status, err)
		}
	}
}

func TestSupportUploadRefusalClassification(t *testing.T) {
	for _, code := range []int{400, 401, 403, 415, 429, 500, 503, 0} {
		err := supportUploadOutcome(outbound.Response{Status: code}, errors.New("private provider detail"))
		var refusal *SupportDeliveryError
		if !errors.As(err, &refusal) || !refusal.Definitive || refusal.Temporary != (code == 0 || code == 429 || code >= 500) {
			t.Fatalf("incorrect upload outcome for HTTP %d", code)
		}
	}
	if supportUploadOutcome(outbound.Response{Status: 200, Body: []byte(`{"success":false,"message":"private detail"}`)}, nil) == nil {
		t.Fatal("200 refusal was accepted")
	}
	for _, body := range []string{`{"success":false}`, `{}`, `<retval>0</retval>`, `invalid`} {
		if supportUploadConfirmed([]byte(body)) {
			t.Fatal("unconfirmed upload accepted")
		}
	}
	if !supportUploadConfirmed([]byte(`<retval>1</retval>`)) {
		t.Fatal("documented upload success rejected")
	}
}
