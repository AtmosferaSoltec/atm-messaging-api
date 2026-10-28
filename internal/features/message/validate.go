package message

import (
	"encoding/base64"
	"net/http"
	"path/filepath"
	"slices"
	"strings"

	"github.com/AtmosferaSoltec/atm-messaging-api/internal/channel"
	"github.com/AtmosferaSoltec/atm-messaging-api/internal/shared/phone"
)

const (
	maxBodyLength     = 4096
	maxCaptionLength  = 1024
	maxAttachmentSize = 16 << 20 // 16 MiB decoded
	maxFilenameLength = 255
)

// attachmentSpec describes an accepted file type: the extension used when the
// request gives no filename, and what the file's real content must look like.
type attachmentSpec struct {
	extension string
	// sniffed lists the media types http.DetectContentType may report for a
	// genuine file of this type. Office files are ZIP containers, for example.
	sniffed []string
}

const (
	mimeDocx = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	mimeXlsx = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	mimePptx = "application/vnd.openxmlformats-officedocument.presentationml.presentation"
)

// allowedAttachments lists what each message kind accepts. The declared type must
// also match the file's real content, so a renamed file is rejected.
var allowedAttachments = map[channel.MessageKind]map[string]attachmentSpec{
	channel.KindImage: {
		"image/jpeg": {".jpg", []string{"image/jpeg"}},
		"image/png":  {".png", []string{"image/png"}},
		"image/webp": {".webp", []string{"image/webp"}},
	},
	channel.KindVideo: {
		"video/mp4": {".mp4", []string{"video/mp4"}},
	},
	channel.KindAudio: {
		"audio/mpeg": {".mp3", []string{"audio/mpeg"}},
		"audio/ogg":  {".ogg", []string{"application/ogg", "audio/ogg"}},
	},
	channel.KindDocument: {
		"application/pdf": {".pdf", []string{"application/pdf"}},
		"text/plain":      {".txt", []string{"text/plain"}},
		"text/csv":        {".csv", []string{"text/plain"}},
		mimeDocx:          {".docx", []string{"application/zip"}},
		mimeXlsx:          {".xlsx", []string{"application/zip"}},
		mimePptx:          {".pptx", []string{"application/zip"}},
	},
}

// ValidationError is a client mistake in the request, with a stable code.
type ValidationError struct {
	Code    string
	Message string
}

func (e *ValidationError) Error() string { return e.Code + ": " + e.Message }

func invalid(code, message string) *ValidationError {
	return &ValidationError{Code: code, Message: message}
}

// validated is a request that passed validation, normalized for use.
type validated struct {
	channel    channel.Type
	recipient  string
	kind       channel.MessageKind
	body       string
	priority   Priority
	attachment *channel.Attachment
}

func validate(req SendRequest) (validated, error) {
	var v validated

	v.channel = channel.Type(req.Channel)
	if v.channel != channel.WhatsApp {
		return v, invalid("unsupported_channel", "unsupported channel")
	}

	recipient, err := phone.Normalize(req.To)
	if err != nil {
		return v, invalid("invalid_recipient", "to must be a phone number in international format (8 to 15 digits)")
	}
	v.recipient = recipient

	v.kind = channel.MessageKind(req.Type)
	switch v.kind {
	case channel.KindText, channel.KindImage, channel.KindVideo, channel.KindAudio, channel.KindDocument:
	default:
		return v, invalid("invalid_type", "type must be text, image, video, audio or document")
	}

	switch Priority(req.Priority) {
	case "", PriorityTransactional:
		v.priority = PriorityTransactional
	case PriorityPromotional:
		v.priority = PriorityPromotional
	default:
		return v, invalid("invalid_priority", "priority must be transactional or promotional")
	}

	v.body = strings.TrimSpace(req.Body)
	if v.kind == channel.KindText {
		if v.body == "" || len(v.body) > maxBodyLength {
			return v, invalid("invalid_body", "body is required for text messages and must be at most 4096 characters")
		}
		if req.Attachment != nil {
			return v, invalid("unexpected_attachment", "text messages cannot have an attachment")
		}
		return v, nil
	}

	if v.kind == channel.KindAudio && v.body != "" {
		return v, invalid("invalid_body", "audio messages cannot have a caption")
	}
	if len(v.body) > maxCaptionLength {
		return v, invalid("invalid_body", "caption must be at most 1024 characters")
	}
	attachment, err := validateAttachment(v.kind, req.Attachment)
	if err != nil {
		return v, err
	}
	v.attachment = attachment
	return v, nil
}

func validateAttachment(kind channel.MessageKind, req *AttachmentRequest) (*channel.Attachment, error) {
	if req == nil {
		return nil, invalid("attachment_required", "image and document messages require an attachment")
	}

	spec, ok := allowedAttachments[kind][req.MimeType]
	if !ok {
		return nil, invalid("unsupported_attachment_type", "attachment mime_type is not allowed for this message type")
	}

	if base64.StdEncoding.DecodedLen(len(req.ContentBase64)) > maxAttachmentSize+3 {
		return nil, invalid("attachment_too_large", "attachment exceeds the 16 MiB limit")
	}
	data, err := base64.StdEncoding.DecodeString(req.ContentBase64)
	if err != nil || len(data) == 0 {
		return nil, invalid("invalid_attachment", "attachment content_base64 is not valid base64")
	}
	if len(data) > maxAttachmentSize {
		return nil, invalid("attachment_too_large", "attachment exceeds the 16 MiB limit")
	}
	// DetectContentType may add parameters ("text/plain; charset=utf-8"); only the media type matters.
	detected, _, _ := strings.Cut(http.DetectContentType(data), ";")
	if !slices.Contains(spec.sniffed, strings.TrimSpace(detected)) {
		return nil, invalid("attachment_type_mismatch", "attachment content does not match its mime_type")
	}

	filename := filepath.Base(strings.TrimSpace(req.Filename))
	if filename == "." || filename == "/" || filename == "" {
		filename = "file" + spec.extension
	}
	if len(filename) > maxFilenameLength {
		return nil, invalid("invalid_filename", "filename must be at most 255 characters")
	}

	return &channel.Attachment{Filename: filename, MimeType: req.MimeType, Data: data}, nil
}
