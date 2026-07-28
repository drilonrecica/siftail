package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/drilonrecica/siftail/internal/logs"
	"github.com/drilonrecica/siftail/internal/sessions"
)

const livePreviewBytes = 8 << 10

var liveParameters = map[string]struct{}{
	"source":   {},
	"level":    {},
	"stream":   {},
	"contains": {},
}

type liveLogPayload struct {
	ID                  int64                 `json:"id"`
	EventAtUS           int64                 `json:"event_at_us"`
	ReceivedAtUS        int64                 `json:"received_at_us"`
	SourceID            int64                 `json:"source_id"`
	ContainerInstanceID int64                 `json:"container_instance_id,omitempty"`
	Source              liveSourcePayload     `json:"source"`
	Container           *liveContainerPayload `json:"container,omitempty"`
	Stream              logs.Stream           `json:"stream"`
	Level               logs.Level            `json:"level"`
	OriginalLevel       string                `json:"level_original,omitempty"`
	Message             string                `json:"message"`
	MessageTruncated    bool                  `json:"message_truncated"`
	AttributesPreview   string                `json:"attributes_preview,omitempty"`
	AttributesTruncated bool                  `json:"attributes_truncated"`
	Common              liveCommonPayload     `json:"common"`
}

type liveSourcePayload struct {
	ServerID         int64  `json:"server_id"`
	Project          string `json:"project"`
	Environment      string `json:"environment"`
	Application      string `json:"application"`
	Service          string `json:"service"`
	ProjectLabel     string `json:"project_label"`
	EnvironmentLabel string `json:"environment_label"`
	ApplicationLabel string `json:"application_label"`
	ServiceLabel     string `json:"service_label"`
}

type liveContainerPayload struct {
	ID   string `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
}

type liveCommonPayload struct {
	Logger     string   `json:"logger,omitempty"`
	RequestID  string   `json:"request_id,omitempty"`
	ErrorType  string   `json:"error_type,omitempty"`
	HTTPMethod string   `json:"http_method,omitempty"`
	HTTPPath   string   `json:"http_path,omitempty"`
	HTTPStatus *int64   `json:"http_status,omitempty"`
	DurationMS *float64 `json:"duration_ms,omitempty"`
}

type liveControlPayload struct {
	Type     string `json:"type"`
	SourceID int64  `json:"source_id,omitempty"`
}

func (b *Browser) liveStream(w http.ResponseWriter, r *http.Request) {
	if b.live == nil {
		http.Error(w, "Live streaming is temporarily unavailable.", http.StatusServiceUnavailable)
		return
	}
	if !acceptsEventStream(r.Header.Get("Accept")) {
		http.Error(w, "Accept text/event-stream is required.", http.StatusNotAcceptable)
		return
	}
	if !b.validLiveOrigin(r) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	filter, err := parseLiveFilter(r.URL.Query())
	if err != nil {
		http.Error(w, "Invalid Live filters.", http.StatusBadRequest)
		return
	}
	reconnecting, err := liveReconnect(r.Header.Get("Last-Event-ID"))
	if err != nil {
		http.Error(w, "Invalid Last-Event-ID.", http.StatusBadRequest)
		return
	}
	if _, ok := w.(http.Flusher); !ok {
		http.Error(w, "Live streaming is unavailable.", http.StatusInternalServerError)
		return
	}
	browserSession, ok := BrowserSessionFromContext(r.Context())
	if !ok {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	subscription, err := b.live.Subscribe(r.Context(), filter)
	if err != nil {
		status := http.StatusServiceUnavailable
		if errors.Is(err, logs.ErrInvalidLiveFilter) {
			status = http.StatusBadRequest
		}
		http.Error(w, "Live streaming is temporarily unavailable.", status)
		return
	}
	defer subscription.Close()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("Content-Encoding", "identity")
	w.Header().Set("Connection", "keep-alive")
	if err := b.writeLiveRaw(w, []byte("retry: 3000\n\n")); err != nil {
		return
	}
	if reconnecting {
		if err := b.writeLiveControl(w, liveControlPayload{Type: "possible_gap"}); err != nil {
			return
		}
	}

	nextHeartbeat := b.now().Add(b.liveHeartbeat)
	nextSessionCheck := b.now().Add(b.liveSessionCheck)
	for {
		now := b.now()
		deadline := nextHeartbeat
		if nextSessionCheck.Before(deadline) {
			deadline = nextSessionCheck
		}
		waitContext, cancel := context.WithDeadline(r.Context(), deadline)
		message, nextErr := subscription.Next(waitContext)
		cancel()

		if nextErr == nil {
			if err := b.writeLiveMessage(w, message); err != nil {
				return
			}
			continue
		}
		if errors.Is(nextErr, context.Canceled) && r.Context().Err() != nil {
			return
		}
		if !errors.Is(nextErr, context.DeadlineExceeded) {
			switch {
			case errors.Is(nextErr, logs.ErrLiveOverflow):
				_ = b.writeLiveControl(w, liveControlPayload{Type: "truncated"})
			case errors.Is(nextErr, logs.ErrLiveBrokerStopped):
				_ = b.writeLiveControl(w, liveControlPayload{Type: "shutdown"})
			}
			return
		}

		now = b.now()
		if !now.Before(nextSessionCheck) {
			if _, lookupErr := b.sessions.Lookup(r.Context(), browserSession.Token); lookupErr != nil {
				controlType := "unavailable"
				if errors.Is(lookupErr, sessions.ErrInvalidSession) {
					controlType = "session_invalid"
				}
				_ = b.writeLiveControl(w, liveControlPayload{Type: controlType})
				return
			}
			nextSessionCheck = now.Add(b.liveSessionCheck)
		}
		if !now.Before(nextHeartbeat) {
			if err := b.writeLiveControl(w, liveControlPayload{Type: "heartbeat"}); err != nil {
				return
			}
			nextHeartbeat = now.Add(b.liveHeartbeat)
		}
	}
}

func (b *Browser) validLiveOrigin(r *http.Request) bool {
	return b.validRequestOrigin(r)
}

func acceptsEventStream(value string) bool {
	for _, entry := range strings.Split(value, ",") {
		mediaType, _, err := mime.ParseMediaType(strings.TrimSpace(entry))
		if err == nil && mediaType == "text/event-stream" {
			return true
		}
	}
	return false
}

func liveReconnect(value string) (bool, error) {
	if value == "" {
		return false, nil
	}
	if len(value) > 20 || strings.ContainsAny(value, "\r\n") {
		return false, errors.New("invalid Last-Event-ID")
	}
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil || id <= 0 {
		return false, errors.New("invalid Last-Event-ID")
	}
	return true, nil
}

func parseLiveFilter(values url.Values) (logs.LiveFilter, error) {
	for key := range values {
		if _, ok := liveParameters[key]; !ok {
			return logs.LiveFilter{}, fmt.Errorf("unknown Live parameter %q", key)
		}
	}
	var filter logs.LiveFilter
	if entries := values["source"]; len(entries) > 256 {
		return logs.LiveFilter{}, errors.New("too many source filters")
	} else {
		for _, entry := range entries {
			id, err := strconv.ParseInt(entry, 10, 64)
			if err != nil || id <= 0 {
				return logs.LiveFilter{}, errors.New("source must be a positive integer")
			}
			filter.SourceIDs = append(filter.SourceIDs, id)
		}
	}
	if entries := values["level"]; len(entries) > 7 {
		return logs.LiveFilter{}, errors.New("too many level filters")
	} else {
		for _, entry := range entries {
			filter.Levels = append(filter.Levels, logs.Level(entry))
		}
	}
	if entries := values["stream"]; len(entries) > 3 {
		return logs.LiveFilter{}, errors.New("too many stream filters")
	} else {
		for _, entry := range entries {
			filter.Streams = append(filter.Streams, logs.Stream(entry))
		}
	}
	if entries := values["contains"]; len(entries) > 1 {
		return logs.LiveFilter{}, errors.New("contains must occur once")
	} else if len(entries) == 1 {
		filter.Contains = entries[0]
		if !utf8.ValidString(filter.Contains) ||
			len(filter.Contains) > logs.MaxTextFilterBytes ||
			strings.ContainsRune(filter.Contains, 0) {
			return logs.LiveFilter{}, errors.New("contains is invalid")
		}
	}
	return filter, nil
}

func (b *Browser) writeLiveMessage(w http.ResponseWriter, message logs.LiveMessage) error {
	if message.Type == logs.LiveMessageControl {
		return b.writeLiveControl(w, liveControlPayload{
			Type: string(message.Control.Type), SourceID: message.Control.SourceID,
		})
	}
	event := message.Event
	messageLimit := 6 << 10
	messagePreview, messageTruncated := liveUTF8Prefix(event.Event.MessageText, messageLimit)
	remaining := livePreviewBytes - len(messagePreview)
	attributesPreview, attributesTruncated := liveUTF8Prefix(string(event.Event.Attributes), remaining)
	payload := liveLogPayload{
		ID: event.ID, EventAtUS: event.Event.EventAtUS,
		ReceivedAtUS: event.Event.ReceivedAtUS,
		SourceID:     event.SourceID, ContainerInstanceID: event.ContainerInstanceID,
		Source: liveSourcePayload{
			ServerID:         event.Event.Source.ServerID,
			Project:          event.Event.Source.Project,
			Environment:      event.Event.Source.Environment,
			Application:      event.Event.Source.Application,
			Service:          event.Event.Source.Service,
			ProjectLabel:     event.Event.Source.ProjectLabel,
			EnvironmentLabel: event.Event.Source.EnvLabel,
			ApplicationLabel: event.Event.Source.AppLabel,
			ServiceLabel:     event.Event.Source.ServiceLabel,
		},
		Stream: event.Event.Stream, Level: event.Event.Level,
		OriginalLevel: event.Event.OriginalLevel,
		Message:       messagePreview, MessageTruncated: messageTruncated,
		AttributesPreview: attributesPreview, AttributesTruncated: attributesTruncated,
		Common: liveCommonPayload{
			Logger:     event.Event.Common.Logger,
			RequestID:  event.Event.Common.RequestID,
			ErrorType:  event.Event.Common.ErrorType,
			HTTPMethod: event.Event.Common.HTTPMethod,
			HTTPPath:   event.Event.Common.HTTPPath,
			HTTPStatus: event.Event.Common.HTTPStatus,
			DurationMS: event.Event.Common.DurationMS,
		},
	}
	if event.Event.Container != nil {
		payload.Container = &liveContainerPayload{
			ID: event.Event.Container.ID, Name: event.Event.Container.Name,
		}
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	frame := fmt.Appendf(nil, "event: log\nid: %d\ndata: ", event.ID)
	frame = append(frame, encoded...)
	frame = append(frame, '\n', '\n')
	return b.writeLiveRaw(w, frame)
}

func (b *Browser) writeLiveControl(w http.ResponseWriter, payload liveControlPayload) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	frame := append([]byte("event: control\ndata: "), encoded...)
	frame = append(frame, '\n', '\n')
	return b.writeLiveRaw(w, frame)
}

func (b *Browser) writeLiveRaw(w http.ResponseWriter, frame []byte) error {
	controller := http.NewResponseController(w)
	_ = controller.SetWriteDeadline(time.Now().Add(b.liveWriteTimeout))
	if _, err := w.Write(frame); err != nil {
		return err
	}
	return controller.Flush()
}

func liveUTF8Prefix(value string, limit int) (string, bool) {
	if len(value) <= limit {
		return value, false
	}
	if limit <= 0 {
		return "", value != ""
	}
	end := limit
	for end > 0 && !utf8.RuneStart(value[end]) {
		end--
	}
	return value[:end], true
}
