package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/go-chi/chi/v5"
	"mindfs/server/internal/fs"
	"mindfs/server/internal/session"
)

func TestHTTPSessionMessageValidationAndQueue(t *testing.T) {
	registry := fs.NewRegistry(filepath.Join(t.TempDir(), "registry.json"))
	root, err := registry.Upsert(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	app := &AppContext{Dirs: registry}
	manager, err := app.GetSessionManager(root.ID)
	if err != nil {
		t.Fatal(err)
	}
	chat, err := manager.Create(context.Background(), session.CreateInput{Type: session.TypeChat, Agent: "codex", Model: "test-model", PlanMode: true})
	if err != nil {
		t.Fatal(err)
	}
	command, err := manager.Create(context.Background(), session.CreateInput{Type: session.TypeCommand})
	if err != nil {
		t.Fatal(err)
	}
	hub := app.GetSessionStreamHub()
	hub.SetPendingReply(root.ID, chat.Key, chat.Name)
	router := chi.NewRouter()
	h := &HTTPHandler{AppContext: app}
	router.Post("/api/sessions/{key}/messages", h.handleSessionUserMessage)
	for _, tc := range []struct {
		key, message string
		status       int
	}{
		{chat.Key, `{"message":"hello"}`, http.StatusAccepted},
		{chat.Key, `{"message":"  "}`, http.StatusBadRequest},
		{chat.Key, `{"message":"hello","extra":true}`, http.StatusBadRequest},
		{"missing", `{"message":"hello"}`, http.StatusNotFound},
		{command.Key, `{"message":"hello"}`, http.StatusConflict},
	} {
		body := strings.Replace(tc.message, "{", `{"root_id":"`+root.ID+`",`, 1)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/sessions/"+tc.key+"/messages", strings.NewReader(body)))
		if w.Code != tc.status {
			t.Fatalf("%s: status=%d body=%s", tc.message, w.Code, w.Body.String())
		}
		if tc.status == http.StatusAccepted {
			var result struct {
				Queued    bool   `json:"queued"`
				RequestID string `json:"request_id"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || !result.Queued || result.RequestID == "" {
				t.Fatalf("invalid acknowledgement: %s", w.Body.String())
			}
		}
	}
	_, queue, _ := hub.queueSnapshot(chat.Key)
	if len(queue) != 1 || queue[0].Content != "hello" || queue[0].Agent != "codex" || queue[0].Model != "test-model" || !queue[0].PlanMode {
		t.Fatalf("incorrect queued user message: %+v", queue)
	}
}

func TestSessionMessageReservationSerializesConcurrentSubmissions(t *testing.T) {
	hub := NewStreamHub(nil)
	var starts atomic.Int32
	var workers sync.WaitGroup
	for i := 0; i < 20; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			_, queued := hub.reserveOrQueueSessionMessage("root", "chat", "Chat", QueuedUserMessage{PendingUserMessage: PendingUserMessage{Content: "message"}})
			if !queued {
				starts.Add(1)
			}
		}()
	}
	workers.Wait()
	_, queue, _ := hub.queueSnapshot("chat")
	if starts.Load() != 1 || len(queue) != 19 {
		t.Fatalf("starts=%d queued=%d", starts.Load(), len(queue))
	}
	if _, _, ok := hub.PopQueuedSessionMessage("chat", ""); ok {
		t.Fatal("popped a second turn while the first was reserved")
	}
}

func TestLocalCLISessionMessagePath(t *testing.T) {
	for _, tc := range []struct {
		method, path string
		want         bool
	}{
		{"POST", "/api/sessions/chat/messages", true},
		{"GET", "/api/sessions/chat/messages", false},
		{"POST", "/api/sessions/chat/rename", false},
		{"POST", "/api/sessions/messages", false},
		{"POST", "/api/sessions/chat/extra/messages", false},
	} {
		if got := isLocalCLIPath(httptest.NewRequest(tc.method, tc.path, nil)); got != tc.want {
			t.Fatalf("%s %s: %v", tc.method, tc.path, got)
		}
	}
}
