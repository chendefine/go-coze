package coze

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

const (
	testToken = ""
	testBotId = ""
)

func TestChat(t *testing.T) {
	// Create a new bot instance
	cfg := &BotConfig{Token: testToken, BotId: testBotId}
	bot := NewBot(cfg)
	msg := []*ChatMessage{{Role: RoleUser, Content: "Hello World!"}}
	req := &ChatReq{Stream: true, AutoSaveHistory: false, AdditionalMessages: msg}

	// Start a chat session
	chat, err := bot.Chat(context.Background(), req)
	if err != nil {
		t.Logf("error: %s\n", err)
		return
	}

	// Read event from chat stream
	for ev := range chat.Stream() {
		data, _ := json.Marshal(ev.Data)
		t.Logf("%s: %s\n", ev.Event, string(data))
	}

	// Get chat final result synchronously
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result := chat.GetResult(ctx)
	t.Logf("%+v\n", result)
}
