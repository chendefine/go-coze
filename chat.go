package coze

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"math/rand"
	"strconv"
	"strings"
	"sync"

	"github.com/panjf2000/ants/v2"
)

const (
	chatEndpoint = "/v3/chat"
)

type (
	Role            = string
	Type            = string
	ContentType     = string
	ContentItemType = string
)

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"

	TypeQuery        Type = "query"
	TypeAnswer       Type = "answer"
	TypeFunctionCall Type = "function_call"
	TypeFollowUp     Type = "follow_up"

	ContentTypeText         ContentType = "text"
	ContentTypeObjectString ContentType = "object_string"

	ContentItemTypeText  ContentItemType = "text"
	ContentItemTypeFile  ContentItemType = "file"
	ContentItemTypeImage ContentItemType = "image"
)

const (
	ChatInProgress   = "conversation.chat.in_progress"
	ChatCompleted    = "conversation.chat.completed"
	ChatFailed       = "conversation.chat.failed"
	MessageDelta     = "conversation.message.delta"
	MessageCompleted = "conversation.message.completed"

	EventStatusCompleted = "completed"
	EventDone            = "done"

	EventPrefixEvent = "event:"
	EventPrefixData  = "data:"
)

type ContentItem struct {
	Type    ContentItemType `json:"type"`
	Text    string          `json:"text,omitempty"`
	FileId  string          `json:"file_id,omitempty"`
	FileUrl string          `json:"file_url,omitempty"`
}

type ChatMessage struct {
	Role        Role           `json:"role"`
	Type        Type           `json:"type,omitempty"`
	Content     any            `json:"content"`
	ContentType string         `json:"content_type,omitempty"`
	MetaData    map[string]any `json:"meta_data,omitempty"`
}

type ChatUsage struct {
	TokenCount  int `json:"token_count"`
	OutputCount int `json:"output_count"`
	InputCount  int `json:"input_count"`
}

type ChatEvent struct {
	Event string         `json:"event"`
	Data  *ChatEventData `json:"data"`
}

type ChatEventData struct {
	Id             string `json:"id"`
	ConversationId string `json:"conversation_id"`
	BotId          string `json:"bot_id"`
	ChatId         string `json:"chat_id,omitempty"`

	Role        Role        `json:"role,omitempty"`
	Type        Type        `json:"type,omitempty"`
	Content     string      `json:"content,omitempty"`
	ContentType ContentType `json:"content_type,omitempty"`

	CompletedAt int64      `json:"completed_at,omitempty"`
	LastError   *ErrorWrap `json:"last_error,omitempty"`
	Status      string     `json:"status,omitempty"`
	Usage       *ChatUsage `json:"usage,omitempty"`
}

type ChatReturnWrap struct {
	*ErrorWrap
	Data *ChatEventData `json:"data"`
}

type ChatResult struct {
	ConversationId string     `json:"conversation_id"`
	ChatId         string     `json:"chat_id"`
	Answer         string     `json:"answer,omitempty"`
	FollowUp       []string   `json:"follow_up,omitempty"`
	CompletedAt    int64      `json:"completed_at,omitempty"`
	Usage          *ChatUsage `json:"usage,omitempty"`
	LastError      *ErrorWrap `json:"last_error,omitempty"`
}

type ChatReq struct {
	BotId              string            `json:"bot_id"`
	UserId             string            `json:"user_id"`
	AdditionalMessages []*ChatMessage    `json:"additional_messages"`
	Stream             bool              `json:"stream,omitempty"`
	AutoSaveHistory    bool              `json:"auto_save_history"`
	CustomVariables    map[string]string `json:"custom_variables,omitempty"`
	MetaData           map[string]any    `json:"meta_data,omitempty"`
}

type ChatRsp struct {
	Return *ChatReturnWrap

	done bool
	cond *sync.Cond
	pool *ants.Pool
	buff []*ChatEvent
}

func (bot *Bot) Chat(ctx context.Context, req *ChatReq) (*ChatRsp, error) {
	req.BotId = bot.config.BotId
	if req.UserId == "" {
		req.UserId = strconv.Itoa(rand.Int())
	}

	for _, msg := range req.AdditionalMessages {
		if items, ok := msg.Content.([]*ContentItem); ok {
			objs, _ := json.Marshal(items)
			msg.Content, msg.ContentType = string(objs), ContentTypeObjectString
		} else if msg.ContentType == "" {
			msg.ContentType = ContentTypeText
		}
	}

	rsp := &ChatRsp{pool: bot.chatPool}
	reqx := bot.client.R().SetContext(ctx).SetBody(req).SetDoNotParseResponse(req.Stream)
	if req.Stream {
		rsp.cond, rsp.buff = sync.NewCond(&sync.Mutex{}), make([]*ChatEvent, 0, 100)
	} else {
		rsp.Return = new(ChatReturnWrap)
		reqx.SetResult(rsp.Return)
	}

	resp, err := reqx.Post(chatEndpoint)
	if err != nil {
		return nil, err
	}

	if req.Stream {
		err = bot.handleChatSync(resp.RawResponse.Body, rsp)
		if err != nil {
			return nil, err
		}
	} else {
		if rsp.Return.ErrorWrap != nil && rsp.Return.ErrorWrap.Code > 0 {
			return nil, rsp.Return.ErrorWrap
		}
		rsp.buff, rsp.done = append(rsp.buff, &ChatEvent{Event: ChatInProgress, Data: rsp.Return.Data}), true
	}

	return rsp, nil
}

func (bot *Bot) handleChatSync(body io.ReadCloser, rsp *ChatRsp) error {
	scanner := bufio.NewScanner(body)

	var event *ChatEvent
	for scanner.Scan() {
		row := scanner.Text()
		if event == nil && !strings.HasPrefix(row, EventPrefixEvent) {
			err := new(ErrorWrap)
			_ = json.Unmarshal([]byte(row), err)
			body.Close()
			return err
		}

		if strings.HasPrefix(row, EventPrefixEvent) {
			event = &ChatEvent{Event: strings.TrimPrefix(row, EventPrefixEvent), Data: new(ChatEventData)}
		} else if strings.HasPrefix(row, EventPrefixData) {
			_ = json.Unmarshal([]byte(strings.TrimPrefix(row, EventPrefixData)), &event.Data)
			rsp.buff = append(rsp.buff, event)
			if event.Event == ChatInProgress {
				rsp.Return = &ChatReturnWrap{Data: event.Data}
				break
			}
		}
	}
	_ = bot.chatPool.Submit(func() { handleScanEvent(scanner, body, &rsp.buff, &rsp.done, rsp.cond) })
	return nil
}

func handleScanEvent(scanner *bufio.Scanner, closer io.Closer, buff *[]*ChatEvent, done *bool, cond *sync.Cond) {
	var event *ChatEvent
	for scanner.Scan() {
		row := scanner.Text()
		if row == "" {
			continue
		}

		if strings.HasPrefix(row, EventPrefixEvent) {
			event = &ChatEvent{Event: strings.TrimPrefix(row, EventPrefixEvent)}
		} else if strings.HasPrefix(row, EventPrefixData) {
			if event.Event != EventDone {
				event.Data = new(ChatEventData)
				_ = json.Unmarshal([]byte(strings.TrimPrefix(row, EventPrefixData)), event.Data)
			}

			cond.L.Lock()
			*buff = append(*buff, event)
			cond.L.Unlock()
			cond.Broadcast()

			if event.Event == EventDone || event.Event == ChatFailed {
				break
			}
		}
	}
	*done = true
	closer.Close()
}

// DO NOT call it after case, it create a new channel every time.
// Please refer to the GetResult() method
func (rsp *ChatRsp) Stream() <-chan *ChatEvent {
	evch := make(chan *ChatEvent)
	_ = rsp.pool.Submit(func() {
		for i := 0; i < len(rsp.buff) || !rsp.done; i++ {
			if i < len(rsp.buff) {
				if rsp.buff[i].Event == EventDone {
					break
				} else {
					evch <- rsp.buff[i]
				}

			} else {
				rsp.cond.L.Lock()
				for i >= len(rsp.buff) {
					rsp.cond.Wait()
				}

				if rsp.buff[i].Event == EventDone {
					rsp.cond.L.Unlock()
					break
				} else {
					evch <- rsp.buff[i]
					rsp.cond.L.Unlock()
				}

			}
		}
		close(evch)
	})
	return evch
}

func (rsp *ChatRsp) GetResult(ctx context.Context) *ChatResult {
	result := new(ChatResult)
	stream := rsp.Stream()
	buff := strings.Builder{}
	for {
		select {
		case <-ctx.Done():
			return result

		case ev, ok := <-stream:
			if !ok {
				return result
			}

			switch ev.Event {
			case ChatInProgress:
				result.ConversationId, result.ChatId = ev.Data.ConversationId, ev.Data.Id
			case ChatCompleted:
				result.Usage, result.CompletedAt = ev.Data.Usage, ev.Data.CompletedAt
			case MessageDelta:
				buff.WriteString(ev.Data.Content)
				result.Answer = buff.String()
			case MessageCompleted:
				switch ev.Data.Type {
				case TypeAnswer:
					result.Answer = ev.Data.Content
				case TypeFollowUp:
					result.FollowUp = append(result.FollowUp, ev.Data.Content)
				}
			case ChatFailed:
				result.LastError = ev.Data.LastError
			}
		}
	}
}
