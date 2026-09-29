package main

import (
	"encoding/json"
	"flag"
	"io"
	"mindfs/server/app"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestToSessionPostsUserMessageWithoutTaskLookup(t *testing.T) {
	for _, key := range []string{"HOME", "USERPROFILE", "AppData", "XDG_CONFIG_HOME"} {
		t.Setenv(key, t.TempDir())
	}
	var token string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/sessions/123/messages" || r.Header.Get("X-MindFS-Local-CLI-Token") != token {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["root_id"] != "root" || body["message"] != "hello" {
			t.Errorf("invalid message: %+v %v", body, err)
		}
		w.WriteHeader(http.StatusAccepted)
		io.WriteString(w, `{"queued":true}`)
	}))
	defer server.Close()
	addr := server.Listener.Addr().String()
	var err error
	token, err = app.EnsureLocalCLIToken(addr, false)
	if err != nil {
		t.Fatal(err)
	}
	input, err := os.CreateTemp(t.TempDir(), "message")
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	if _, err := input.WriteString(`{"message":"hello"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := input.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	previous := os.Stdin
	os.Stdin = input
	defer func() { os.Stdin = previous }()
	if err := handleTaskOperation(addr, false, "root", "123", "to-session", ""); err != nil {
		t.Fatal(err)
	}
}

func TestTaskOperationsDiscoverServiceTLS(t *testing.T) {
	for _, useTLS := range []bool{false, true} {
		name := "http"
		if useTLS {
			name = "https"
		}
		t.Run(name, func(t *testing.T) {
			for _, key := range []string{"HOME", "USERPROFILE", "AppData", "XDG_CONFIG_HOME"} {
				t.Setenv(key, t.TempDir())
			}
			var token string
			var paths []string
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("X-MindFS-Local-CLI-Token") != token || (r.TLS != nil) != useTLS {
					t.Error("incorrect authentication or transport")
				}
				paths = append(paths, r.URL.Path)
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/api/tasks" {
					io.WriteString(w, `{"items":[{"task":{"id":"task-id","task_number":7}}]}`)
					return
				}
				io.WriteString(w, `{}`)
			})
			server := httptest.NewUnstartedServer(handler)
			if useTLS {
				server.StartTLS()
			} else {
				server.Start()
			}
			defer server.Close()
			addr := server.Listener.Addr().String()
			var err error
			token, err = app.EnsureLocalCLIToken(addr, useTLS)
			if err != nil {
				t.Fatal(err)
			}
			resolved, err := resolveClientTLS(addr, false, false)
			if err != nil || resolved != useTLS {
				t.Fatalf("resolved TLS = %v, %v", resolved, err)
			}
			for _, explicit := range []bool{false, true} {
				if got, err := resolveClientTLS(addr, explicit, true); err != nil || got != explicit {
					t.Fatalf("explicit TLS ignored: %v, %v", got, err)
				}
			}
			input, err := os.CreateTemp(t.TempDir(), "request")
			if err != nil {
				t.Fatal(err)
			}
			defer input.Close()
			if _, err := input.WriteString(`{"parent_session_key":"parent"}`); err != nil {
				t.Fatal(err)
			}
			if _, err := input.Seek(0, 0); err != nil {
				t.Fatal(err)
			}
			originalStdin := os.Stdin
			os.Stdin = input
			defer func() { os.Stdin = originalStdin }()
			if err := handleTaskOperation(addr, resolved, "root", "", "group:create", ""); err != nil {
				t.Fatal(err)
			}
			if err := handleTaskOperation(addr, resolved, "root", "7", "status", ""); err != nil {
				t.Fatal(err)
			}
			if got := strings.Join(paths, ","); got != "/api/task-groups,/api/tasks,/api/tasks/task-id" {
				t.Fatalf("unexpected requests: %s", got)
			}
		})
	}
}

func TestReadTaskJSON(t *testing.T) {
	for _, tc := range []struct {
		name, input                      string
		interactive, required, wantError bool
	}{
		{name: "heredoc", input: "{\n\"input\":\"hello\\nworld\"\n}", required: true},
		{name: "empty optional"},
		{name: "whitespace optional", input: " \n"},
		{name: "empty required", required: true, wantError: true},
		{name: "interactive required", interactive: true, required: true, wantError: true},
		{name: "interactive optional", interactive: true},
		{name: "null", input: "null", wantError: true},
		{name: "array", input: "[]", wantError: true},
		{name: "multiple objects", input: "{} {}", wantError: true},
		{name: "invalid JSON", input: "{", wantError: true},
		{name: "oversized", input: strings.Repeat(" ", (4<<20)+1), wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var body map[string]any
			var err error
			if tc.interactive {
				body, err = readTaskJSON(noReadInput{t}, true, tc.required)
			} else {
				body, err = readTaskJSON(strings.NewReader(tc.input), false, tc.required)
			}
			if (err != nil) != tc.wantError {
				t.Fatalf("error=%v", err)
			}
			if err == nil && body == nil {
				t.Fatal("nil object")
			}
			if tc.name == "heredoc" && body["input"] != "hello\nworld" {
				t.Fatalf("multiline content lost: %v", body)
			}
		})
	}
}

type noReadInput struct{ t *testing.T }

func (r noReadInput) Read([]byte) (int, error) {
	r.t.Fatal("interactive stdin must not be read")
	return 0, nil
}

func TestDirectTaskOperationFlags(t *testing.T) {
	for _, tc := range []struct {
		name       string
		args       []string
		scope      bool
		want       string
		count      int
		parseError bool
	}{
		{"graph", []string{"-graph"}, true, "graph", 1, false},
		{"plan", []string{"-plan"}, true, "plan", 1, false},
		{"task update", []string{"-update"}, true, "update", 1, false},
		{"service update", []string{"-update"}, false, "", 0, false},
		{"task status", []string{"-status"}, true, "status", 1, false},
		{"service status", []string{"-status"}, false, "", 0, false},
		{"next", []string{"-next"}, true, "next", 1, false},
		{"default", nil, true, "", 0, false},
		{"conflict", []string{"-publish", "-cancel"}, true, "", 2, false},
		{"status conflict", []string{"-status", "-next"}, true, "", 2, false},
		{"old syntax removed", []string{"-action", "graph"}, true, "", 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fs := flag.NewFlagSet("test", flag.ContinueOnError)
			fs.SetOutput(io.Discard)
			status := fs.Bool("status", false, "")
			update := fs.Bool("update", false, "")
			operations := registerTaskOperationFlags(fs, status, update)
			err := fs.Parse(tc.args)
			if (err != nil) != tc.parseError {
				t.Fatalf("parse error=%v", err)
			}
			if err != nil {
				return
			}
			action, count := selectedTaskOperation(operations, tc.scope)
			if count != tc.count || (count < 2 && action != tc.want) {
				t.Fatalf("action=%s count=%d", action, count)
			}
		})
	}
}
