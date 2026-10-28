package whatsapp

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/protobuf/proto"

	"github.com/AtmosferaSoltec/atm-messaging-api/internal/channel"
)

// Send delivers a message through the session's WhatsApp connection.
func (m *Manager) Send(ctx context.Context, sessionID uuid.UUID, msg channel.OutboundMessage) (channel.SendResult, error) {
	client, ok := m.client(sessionID)
	if !ok || !client.IsConnected() || client.Store.ID == nil {
		return channel.SendResult{}, channel.ErrSessionNotConnected
	}

	reachable, err := client.IsOnWhatsApp(ctx, []string{"+" + msg.To})
	if err != nil {
		return channel.SendResult{}, fmt.Errorf("check recipient: %w", err)
	}
	if len(reachable) == 0 || !reachable[0].IsIn {
		return channel.SendResult{}, channel.ErrRecipientNotFound
	}
	to := reachable[0].JID

	content, err := buildContent(ctx, client, msg)
	if err != nil {
		return channel.SendResult{}, err
	}
	resp, err := client.SendMessage(ctx, to, content)
	if err != nil {
		return channel.SendResult{}, fmt.Errorf("send message: %w", err)
	}
	return channel.SendResult{ProviderMessageID: resp.ID}, nil
}

func buildContent(ctx context.Context, client *whatsmeow.Client, msg channel.OutboundMessage) (*waE2E.Message, error) {
	switch msg.Kind {
	case channel.KindText:
		return &waE2E.Message{Conversation: proto.String(msg.Body)}, nil

	case channel.KindImage:
		upload, err := upload(ctx, client, msg.Attachment, whatsmeow.MediaImage)
		if err != nil {
			return nil, err
		}
		return &waE2E.Message{ImageMessage: &waE2E.ImageMessage{
			URL:           proto.String(upload.URL),
			DirectPath:    proto.String(upload.DirectPath),
			MediaKey:      upload.MediaKey,
			FileEncSHA256: upload.FileEncSHA256,
			FileSHA256:    upload.FileSHA256,
			FileLength:    proto.Uint64(upload.FileLength),
			Mimetype:      proto.String(msg.Attachment.MimeType),
			Caption:       optionalString(msg.Body),
		}}, nil

	case channel.KindDocument:
		upload, err := upload(ctx, client, msg.Attachment, whatsmeow.MediaDocument)
		if err != nil {
			return nil, err
		}
		return &waE2E.Message{DocumentMessage: &waE2E.DocumentMessage{
			URL:           proto.String(upload.URL),
			DirectPath:    proto.String(upload.DirectPath),
			MediaKey:      upload.MediaKey,
			FileEncSHA256: upload.FileEncSHA256,
			FileSHA256:    upload.FileSHA256,
			FileLength:    proto.Uint64(upload.FileLength),
			Mimetype:      proto.String(msg.Attachment.MimeType),
			FileName:      proto.String(msg.Attachment.Filename),
			Caption:       optionalString(msg.Body),
		}}, nil
	}
	return nil, fmt.Errorf("unsupported message kind %q", msg.Kind)
}

func upload(ctx context.Context, client *whatsmeow.Client, attachment *channel.Attachment, mediaType whatsmeow.MediaType) (whatsmeow.UploadResponse, error) {
	if attachment == nil {
		return whatsmeow.UploadResponse{}, fmt.Errorf("attachment is required")
	}
	resp, err := client.Upload(ctx, attachment.Data, mediaType)
	if err != nil {
		return whatsmeow.UploadResponse{}, fmt.Errorf("upload attachment: %w", err)
	}
	return resp, nil
}

func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return proto.String(value)
}
