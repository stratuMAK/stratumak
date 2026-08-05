// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2

// Server lifecycle, introspection endpoint, static webapps and the stream
// upgrade path. These are the parts of the REST surface that outlive a single
// request, so the tests care about shutdown ordering (no goroutine may still be
// calling into a module after Shutdown returns) as much as about responses.
package apiserver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"github.com/coder/websocket"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// --- Listen / serve / shutdown ---

func TestServeAndShutdown(t *testing.T) {
	s := NewServer(NewRegistry(), "127.0.0.1:0")
	s.SetLogger(quietLogger())

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	errCh := make(chan error, 1)
	go func() { errCh <- s.Serve(ln) }()

	url := "http://" + ln.Addr().String() + "/api/v1/nope"
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	select {
	case err := <-errCh:
		if err != http.ErrServerClosed {
			t.Errorf("Serve returned %v, want ErrServerClosed", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return after Shutdown")
	}
}

func TestSetAddrAndListenAndServe(t *testing.T) {
	s := NewServer(NewRegistry(), "127.0.0.1:0")
	s.SetLogger(quietLogger())

	// Take a port, then point the server at it so ListenAndServe is guaranteed
	// to fail — the alternative (binding a real port) would make the test racy.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	s.SetAddr(ln.Addr().String())
	if s.server.Addr != ln.Addr().String() {
		t.Fatalf("SetAddr did not take effect: %q", s.server.Addr)
	}
	if err := s.ListenAndServe(); err == nil {
		t.Error("ListenAndServe on an occupied port must fail")
	}
}

// TestShutdownWaitsForStreamConnections is the safety property behind the
// stream bookkeeping: Shutdown must not return while a ServeConn is still
// running, or the launcher would destroy modules with cgo calls in flight.
func TestShutdownWaitsForStreamConnections(t *testing.T) {
	s := NewServer(NewRegistry(), "127.0.0.1:0")
	s.SetLogger(quietLogger())

	serving := make(chan struct{})
	released := make(chan struct{})
	var once sync.Once
	s.RegisterStream("bulk", "inst", streamServerFunc(func(conn StreamConn) {
		once.Do(func() { close(serving) })
		<-conn.Done() // released by Shutdown cancelling the conn
		close(released)
	}))

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	go func() { _ = s.Serve(ln) }()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	wsURL := "ws://" + ln.Addr().String() + "/api/v1/stream/bulk/inst"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close(websocket.StatusNormalClosure, "") }()

	select {
	case <-serving:
	case <-time.After(5 * time.Second):
		t.Fatal("ServeConn was never called")
	}

	if err := s.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	select {
	case <-released:
	default:
		t.Fatal("Shutdown returned while ServeConn was still running")
	}
}

type streamServerFunc func(StreamConn)

func (f streamServerFunc) ServeConn(c StreamConn) { f(c) }

// TestStreamConnRoundTrip covers the StreamConn implementation the generated
// stream bridges write through.
func TestStreamConnRoundTrip(t *testing.T) {
	s := NewServer(NewRegistry(), "127.0.0.1:0")
	s.SetLogger(quietLogger())

	done := make(chan error, 1)
	s.RegisterStream("echo", "inst", streamServerFunc(func(conn StreamConn) {
		data, err := conn.ReadBinary()
		if err != nil {
			done <- err
			return
		}
		done <- conn.WriteBinary(append([]byte("echo:"), data...))
	}))

	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/v1/stream/echo/inst"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close(websocket.StatusNormalClosure, "") }()

	if err := conn.Write(ctx, websocket.MessageBinary, []byte("hello")); err != nil {
		t.Fatalf("write: %v", err)
	}
	typ, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if typ != websocket.MessageBinary {
		t.Errorf("frame type = %v, want binary", typ)
	}
	if string(data) != "echo:hello" {
		t.Errorf("payload = %q", data)
	}
	if err := <-done; err != nil {
		t.Errorf("ServeConn: %v", err)
	}
}

// TestStreamUpgradeRejectsCrossOrigin mirrors the watch endpoint's same-origin
// default (finding N1) for the stream endpoint.
func TestStreamUpgradeRejectsCrossOrigin(t *testing.T) {
	s := NewServer(NewRegistry(), "127.0.0.1:0")
	s.SetLogger(quietLogger())

	var served bool
	s.RegisterStream("guard", "inst", streamServerFunc(func(StreamConn) { served = true }))

	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/v1/stream/guard/inst"
	_, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		HTTPHeader: http.Header{"Origin": []string{"http://evil.example"}},
	})
	if err == nil {
		t.Fatal("a cross-origin stream upgrade was accepted")
	}
	if served {
		t.Error("ServeConn ran for a rejected upgrade")
	}

	// The explicit allow-list opens it again.
	s2 := NewServer(NewRegistry(), "127.0.0.1:0")
	s2.SetLogger(quietLogger())
	s2.SetWSOriginPatterns([]string{"*"})
	ok := make(chan struct{})
	s2.RegisterStream("guard", "inst", streamServerFunc(func(StreamConn) { close(ok) }))
	srv2 := httptest.NewServer(s2.Handler())
	defer srv2.Close()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv2.URL, "http")+"/api/v1/stream/guard/inst",
		&websocket.DialOptions{HTTPHeader: http.Header{"Origin": []string{"http://evil.example"}}})
	if err != nil {
		t.Fatalf("dial with an explicit allow-list: %v", err)
	}
	defer func() { _ = conn.Close(websocket.StatusNormalClosure, "") }()
	select {
	case <-ok:
	case <-time.After(5 * time.Second):
		t.Error("ServeConn was not called for an allow-listed origin")
	}
}

// --- Registry introspection endpoint ---

func TestRegistryEndpoint(t *testing.T) {
	origWatch := defaultWatchRegistry
	t.Cleanup(func() { defaultWatchRegistry = origWatch })

	reg := NewRegistry()
	meta := &APIMeta{
		Name:       "things",
		Version:    1,
		RESTExport: true,
		Funcs: []FuncMeta{
			{Name: "list", Method: "GET", Path: "/things", Dispatch: mockDispatch("list")},
			{Name: "internal_only", Dispatch: mockDispatch("x")},
		},
	}
	RegisterMeta(meta)
	if err := reg.Register("things", 1, "inst", fakeCallbacks); err != nil {
		t.Fatalf("Register: %v", err)
	}
	reg.RecordConsumer("consumerMod", "things", "inst")

	wreg := NewWatchRegistry()
	wreg.Register(&WatchAPI{
		APIName:  "things",
		Instance: "inst",
		Watches:  []WatchFuncMeta{{Name: "watch_things", DefaultRate: 250 * time.Millisecond}},
		Commands: []CommandMeta{{Name: "poke"}},
	})
	// A watch-only API (no REST registration) must still be listed, otherwise
	// an HMI cannot discover it.
	wreg.Register(&WatchAPI{
		APIName:  "watchonly",
		Instance: "wo",
		Watches:  []WatchFuncMeta{{Name: "w", DefaultRate: 50 * time.Millisecond}},
		Commands: []CommandMeta{{Name: "c"}},
	})
	SetDefaultWatchRegistry(wreg)

	s := NewServer(reg, "127.0.0.1:0")
	s.SetLogger(quietLogger())
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/_registry")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var infos []registryAPIInfo
	if err := json.NewDecoder(resp.Body).Decode(&infos); err != nil {
		t.Fatalf("decode: %v", err)
	}

	byKey := map[string]registryAPIInfo{}
	for _, i := range infos {
		byKey[i.APIName+"/"+i.Instance] = i
	}

	things, ok := byKey["things/inst"]
	if !ok {
		t.Fatalf("the registered API is missing from %+v", infos)
	}
	if !things.REST || things.Version != 1 {
		t.Errorf("things = %+v", things)
	}
	if len(things.Functions) != 2 {
		t.Fatalf("functions = %+v", things.Functions)
	}
	// A REST-exported function's path is reported fully qualified so the HMI
	// can call it directly; a non-exported one carries no path.
	var listed, internal registryFuncInfo
	for _, f := range things.Functions {
		if f.Name == "list" {
			listed = f
		} else {
			internal = f
		}
	}
	if listed.Path != "/api/v1/inst/things" {
		t.Errorf("list path = %q", listed.Path)
	}
	if internal.Path != "" || internal.Method != "" {
		t.Errorf("non-exported function leaked routing info: %+v", internal)
	}
	if len(things.Watches) != 1 || things.Watches[0].DefaultRate != 250 {
		t.Errorf("watches = %+v (rate must be in ms)", things.Watches)
	}
	if len(things.Commands) != 1 || things.Commands[0] != "poke" {
		t.Errorf("commands = %+v", things.Commands)
	}
	if len(things.Consumers) != 1 || things.Consumers[0] != "consumerMod" {
		t.Errorf("consumers = %+v", things.Consumers)
	}

	wo, ok := byKey["watchonly/wo"]
	if !ok {
		t.Fatalf("the watch-only API is missing from %+v", infos)
	}
	if wo.REST || len(wo.Watches) != 1 || len(wo.Commands) != 1 {
		t.Errorf("watch-only entry = %+v", wo)
	}
}

func TestRegistryEndpointRejectsNonGET(t *testing.T) {
	s := NewServer(NewRegistry(), "127.0.0.1:0")
	s.SetLogger(quietLogger())
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/v1/_registry", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", resp.StatusCode)
	}
}

// --- Path-param merging ---

func TestMergePathParamsIntoBody(t *testing.T) {
	tests := []struct {
		name   string
		params map[string]string
		body   string
		want   map[string]interface{}
	}{
		{
			name:   "empty body",
			params: map[string]string{"thread": "servo"},
			body:   "",
			want:   map[string]interface{}{"thread": "servo"},
		},
		{
			name:   "merged into an object",
			params: map[string]string{"thread": "servo"},
			body:   `{"function":"f"}`,
			want:   map[string]interface{}{"thread": "servo", "function": "f"},
		},
		{
			// The body is the explicit request; a path param must not silently
			// override what the caller sent.
			name:   "body wins",
			params: map[string]string{"name": "frompath"},
			body:   `{"name":"frombody"}`,
			want:   map[string]interface{}{"name": "frombody"},
		},
		{
			// Numeric path segments become JSON numbers so a generated wrapper
			// can unmarshal them into an int/float field.
			name:   "numeric coercion",
			params: map[string]string{"channel": "3", "level": "1.5"},
			body:   "",
			want:   map[string]interface{}{"channel": float64(3), "level": 1.5},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := mergePathParamsIntoBody(tt.params, []byte(tt.body))
			var got map[string]interface{}
			if err := json.Unmarshal(out, &got); err != nil {
				t.Fatalf("result is not JSON: %v (%s)", err, out)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for k, want := range tt.want {
				if got[k] != want {
					t.Errorf("key %q = %v (%T), want %v", k, got[k], got[k], want)
				}
			}
		})
	}

	// A non-JSON body is passed through untouched rather than being replaced by
	// a params-only object that would lose the caller's payload.
	if got := mergePathParamsIntoBody(map[string]string{"a": "1"}, []byte("not json")); string(got) != "not json" {
		t.Errorf("non-JSON body = %q, want it passed through", got)
	}
}

// TestPostMergesPathParams drives the same merge through the HTTP layer: a
// dispatch wrapper reads path params out of the body via the same getField()
// as ordinary body params, so the merge has to happen before dispatch.
func TestPostMergesPathParams(t *testing.T) {
	var seen []byte
	reg := NewRegistry()
	meta := &APIMeta{
		Name:       "mergeapi",
		Version:    1,
		RESTExport: true,
		Funcs: []FuncMeta{{
			Name: "addf", Method: "POST", Path: "/thread/{thread}/function",
			Dispatch: func(_ unsafe.Pointer, req []byte) ([]byte, error) {
				seen = append([]byte(nil), req...)
				return []byte(`{"ok":true}`), nil
			},
		}},
	}
	RegisterMeta(meta)
	if err := reg.Register("mergeapi", 1, "minst", fakeCallbacks); err != nil {
		t.Fatalf("Register: %v", err)
	}

	s := NewServer(reg, "127.0.0.1:0")
	s.SetLogger(quietLogger())
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/v1/minst/thread/servo-thread/function",
		"application/json", strings.NewReader(`{"function":"motion.update"}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	var got map[string]interface{}
	if err := json.Unmarshal(seen, &got); err != nil {
		t.Fatalf("dispatch body is not JSON: %v (%s)", err, seen)
	}
	if got["thread"] != "servo-thread" || got["function"] != "motion.update" {
		t.Errorf("dispatch saw %v, want the path param merged with the body", got)
	}
}

// --- Web apps ---

func TestAddWebApps(t *testing.T) {
	dir := t.TempDir()
	appDir := filepath.Join(dir, "hmi")
	if err := os.MkdirAll(filepath.Join(appDir, "assets"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(appDir, "index.html"), []byte("<h1>hmi</h1>"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.WriteFile(filepath.Join(appDir, "assets", "app.js"), []byte("//js"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	// A stray file at the top level is not an app and must be ignored.
	if err := os.WriteFile(filepath.Join(dir, "README"), []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	s := NewServer(NewRegistry(), "127.0.0.1:0")
	s.SetLogger(quietLogger())
	s.AddWebApps(dir)

	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	get := func(path string) (int, string) {
		t.Helper()
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		defer func() { _ = resp.Body.Close() }()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(body)
	}

	if code, body := get("/app/hmi/"); code != http.StatusOK || !strings.Contains(body, "<h1>hmi</h1>") {
		t.Errorf("index = %d %q", code, body)
	}
	if code, body := get("/app/hmi/assets/app.js"); code != http.StatusOK || body != "//js" {
		t.Errorf("asset = %d %q", code, body)
	}
	// SPA fallback: an unknown path inside the app serves index.html so
	// client-side routing works on a hard refresh.
	if code, body := get("/app/hmi/settings/network"); code != http.StatusOK || !strings.Contains(body, "<h1>hmi</h1>") {
		t.Errorf("SPA fallback = %d %q", code, body)
	}
	// The root lists the apps.
	if code, body := get("/"); code != http.StatusOK || !strings.Contains(body, `href="/app/hmi/"`) {
		t.Errorf("root listing = %d %q", code, body)
	}
	// Anything else off the root is a 404, not a listing.
	if code, _ := get("/random"); code != http.StatusNotFound {
		t.Errorf("unknown root path = %d, want 404", code)
	}
}

func TestAddWebAppsTolerantOfBadDir(t *testing.T) {
	// A missing or unset webapp directory must degrade to "no apps", not panic
	// or abort startup — a headless controller has no HMI bundle.
	s := NewServer(NewRegistry(), "127.0.0.1:0")
	s.SetLogger(quietLogger())
	s.AddWebApps("")
	s.AddWebApps(filepath.Join(t.TempDir(), "does-not-exist"))

	// An existing but empty directory still installs the root handler.
	s2 := NewServer(NewRegistry(), "127.0.0.1:0")
	s2.SetLogger(quietLogger())
	s2.AddWebApps(t.TempDir())
	srv := httptest.NewServer(s2.Handler())
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}

// --- Error mapping ---

func TestWriteDispatchErrorErrnoMapping(t *testing.T) {
	for err, want := range map[error]int{
		syscall.EINVAL:  http.StatusBadRequest,
		syscall.ENOENT:  http.StatusNotFound,
		syscall.EPERM:   http.StatusForbidden,
		syscall.EEXIST:  http.StatusConflict,
		syscall.ENOSYS:  http.StatusNotImplemented,
		syscall.EBUSY:   http.StatusConflict,
		syscall.ERANGE:  http.StatusBadRequest,
		syscall.EACCES:  http.StatusInternalServerError, // unmapped → 500
		syscall.EIO:     http.StatusInternalServerError,
		syscall.ENOTSUP: http.StatusInternalServerError,
	} {
		rec := httptest.NewRecorder()
		writeDispatchError(rec, err)
		if rec.Code != want {
			t.Errorf("writeDispatchError(%v) = %d, want %d", err, rec.Code, want)
		}
		var body apiError
		if e := json.Unmarshal(rec.Body.Bytes(), &body); e != nil {
			t.Errorf("body for %v is not JSON: %v", err, e)
		}
		if body.Code != want || body.Error == "" {
			t.Errorf("body for %v = %+v", err, body)
		}
	}
}

// TestWriteDispatchErrorRequestDecode pins the request-decode error contract:
// every generated dispatcher (bridge, server-go, cgo) reports a request that
// fails to unmarshal as fmt.Errorf("%w: %v", syscall.EINVAL, jsonErr) — EINVAL
// in the wrap chain so the transport maps it to 400 (not a 500 provider
// fault), with the json detail preserved in the message. This is the toolno
// wart fix: GET /api/v1/milltask/status matched tools' GET /{toolno} and the
// bridge's raw json error surfaced as a 500.
func TestWriteDispatchErrorRequestDecode(t *testing.T) {
	var params struct {
		Toolno int32 `json:"toolno"`
	}
	jsonErr := json.Unmarshal([]byte(`{"toolno":"status"}`), &params)
	if jsonErr == nil {
		t.Fatal("expected unmarshal type error")
	}
	rec := httptest.NewRecorder()
	writeDispatchError(rec, fmt.Errorf("%w: %v", syscall.EINVAL, jsonErr))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("wrapped EINVAL decode error = %d, want 400", rec.Code)
	}
	var body apiError
	if e := json.Unmarshal(rec.Body.Bytes(), &body); e != nil {
		t.Fatalf("body is not JSON: %v", e)
	}
	if !strings.Contains(body.Error, "toolno") {
		t.Errorf("json detail lost from error body: %q", body.Error)
	}
}

func TestValidationErrorMessage(t *testing.T) {
	ve := NewValidationError("entry.diameter", "min", "entry.diameter: 0 < 1")
	if ve.Error() != "entry.diameter: 0 < 1" {
		t.Errorf("Error() = %q", ve.Error())
	}
}
