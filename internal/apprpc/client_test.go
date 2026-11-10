package apprpc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestClientHandlesHandshakeAndInterleavedNotification(t *testing.T) {
	clientRead, serverWrite := io.Pipe()
	serverRead, clientWrite := io.Pipe()
	defer clientRead.Close()
	defer serverWrite.Close()
	defer serverRead.Close()
	defer clientWrite.Close()

	client := NewClient(clientRead, clientWrite, 64*1024)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	go func() {
		scanner := bufio.NewScanner(serverRead)
		if !scanner.Scan() {
			return
		}
		var initReq map[string]any
		_ = json.Unmarshal(scanner.Bytes(), &initReq)
		_, _ = io.WriteString(serverWrite, `{"method":"account/rateLimits/updated","params":{"rateLimits":{}}}`+"\n")
		_, _ = io.WriteString(serverWrite, `{"id":1,"result":{"serverInfo":{"name":"fake"}}}`+"\n")
		// io.Pipe has no kernel buffer, so consume the initialized notification.
		// A real app-server also continuously reads the client stream.
		_ = scanner.Scan()
	}()

	if err := client.Initialize(ctx, "codexpool-test", "CodexPool Test", "0.0.0"); err != nil {
		t.Fatal(err)
	}
	select {
	case n := <-client.Notifications():
		if n.Method != "account/rateLimits/updated" {
			t.Fatalf("unexpected notification: %s", n.Method)
		}
	case <-ctx.Done():
		t.Fatal("notification not delivered")
	}
}

func TestClientRejectsOversizedInboundFrame(t *testing.T) {
	clientRead, serverWrite := io.Pipe()
	serverRead, clientWrite := io.Pipe()
	defer clientRead.Close()
	defer serverWrite.Close()
	defer serverRead.Close()
	defer clientWrite.Close()

	client := NewClient(clientRead, clientWrite, 128)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	go func() {
		scanner := bufio.NewScanner(serverRead)
		if !scanner.Scan() {
			return
		}
		_, _ = io.WriteString(serverWrite, strings.Repeat("x", 256)+"\n")
	}()

	var out map[string]any
	err := client.Call(ctx, "test", map[string]any{}, &out)
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("expected bounded-frame error, got %v", err)
	}
}

func TestClientRejectsServerInitiatedRequest(t *testing.T) {
	clientRead, serverWrite := io.Pipe()
	serverRead, clientWrite := io.Pipe()
	defer clientRead.Close()
	defer serverWrite.Close()
	defer serverRead.Close()
	defer clientWrite.Close()

	client := NewClient(clientRead, clientWrite, 64*1024)
	go func() {
		_, _ = io.WriteString(serverWrite, `{"id":99,"method":"dangerous/action","params":{}}`+"\n")
	}()
	serverScanner := bufio.NewScanner(serverRead)
	if !serverScanner.Scan() {
		t.Fatal("no rejection response")
	}
	if !strings.Contains(serverScanner.Text(), `"code":-32601`) {
		t.Fatalf("unexpected rejection: %s", serverScanner.Text())
	}
	client.Close()
}

func TestClientFailsPendingCallOnTruncatedFrame(t *testing.T) {
	clientRead, serverWrite := io.Pipe()
	serverRead, clientWrite := io.Pipe()
	defer clientRead.Close()
	defer serverRead.Close()
	defer clientWrite.Close()

	client := NewClient(clientRead, clientWrite, 64*1024)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go func() {
		scanner := bufio.NewScanner(serverRead)
		if !scanner.Scan() {
			return
		}
		_, _ = io.WriteString(serverWrite, `{"id":1,"result":`)
		_ = serverWrite.Close()
	}()

	var out map[string]any
	err := client.Call(ctx, "test", nil, &out)
	if err == nil || !strings.Contains(err.Error(), "truncated RPC frame") {
		t.Fatalf("expected truncated-frame failure, got %v", err)
	}
}

func TestPreCanceledCallDoesNotWriteRequest(t *testing.T) {
	clientRead, serverWrite := io.Pipe()
	defer serverWrite.Close()
	writer := &countingWriter{}
	client := NewClient(clientRead, writer, 64*1024)
	defer client.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out map[string]any
	if err := client.Call(ctx, "test", nil, &out); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-canceled call returned %v", err)
	}
	if writer.Count() != 0 {
		t.Fatalf("pre-canceled call wrote %d RPC frames", writer.Count())
	}
}

func TestClientRejectsShortWriteWithoutError(t *testing.T) {
	clientRead, serverWrite := io.Pipe()
	defer serverWrite.Close()
	client := NewClient(clientRead, shortWriter{}, 64*1024)
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var out map[string]any
	if err := client.Call(ctx, "test", nil, &out); err == nil || !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("short write without error must fail, got %v", err)
	}
}

func TestFinalCompleteResponseSurvivesImmediateEOF(t *testing.T) {
	clientRead, serverWrite := io.Pipe()
	serverRead, clientWrite := io.Pipe()
	defer serverRead.Close()
	defer clientWrite.Close()
	client := NewClient(clientRead, clientWrite, 64*1024)
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go func() {
		scanner := bufio.NewScanner(serverRead)
		if !scanner.Scan() {
			return
		}
		_, _ = io.WriteString(serverWrite, `{"id":1,"result":{"ok":true}}`+"\n")
		_ = serverWrite.Close()
	}()
	var out struct {
		OK bool `json:"ok"`
	}
	if err := client.Call(ctx, "test", nil, &out); err != nil {
		t.Fatalf("complete response immediately followed by EOF was lost: %v", err)
	}
	if !out.OK {
		t.Fatal("complete response payload was not delivered before EOF")
	}
}

func TestCallPrefersBufferedResponseWhenContextAlsoCanceled(t *testing.T) {
	client := &Client{done: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ch := make(chan responseEnvelope, 1)
	ch <- responseEnvelope{Result: json.RawMessage(`{"ok":true}`)}
	var out struct {
		OK bool `json:"ok"`
	}
	if err := client.waitCallResponse(ctx, ch, "test", &out); err != nil {
		t.Fatalf("buffered complete response should win over simultaneous cancellation: %v", err)
	}
	if !out.OK {
		t.Fatal("buffered response payload was lost during cancellation race")
	}
}

func TestCallPrefersBufferedResponseWhenCloseAlsoOccurs(t *testing.T) {
	client := &Client{done: make(chan struct{})}
	client.failAll(io.EOF)
	ch := make(chan responseEnvelope, 1)
	ch <- responseEnvelope{Result: json.RawMessage(`{"ok":true}`)}
	var out struct {
		OK bool `json:"ok"`
	}
	if err := client.waitCallResponse(context.Background(), ch, "test", &out); err != nil {
		t.Fatalf("buffered complete response should win over simultaneous close: %v", err)
	}
	if !out.OK {
		t.Fatal("buffered response payload was lost during close race")
	}
}

func TestNotificationFloodCannotBlockResponseDelivery(t *testing.T) {
	clientRead, serverWrite := io.Pipe()
	serverRead, clientWrite := io.Pipe()
	defer serverRead.Close()
	defer clientWrite.Close()
	client := NewClient(clientRead, clientWrite, 64*1024)
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go func() {
		scanner := bufio.NewScanner(serverRead)
		if !scanner.Scan() {
			return
		}
		for i := 0; i < 256; i++ {
			_, _ = io.WriteString(serverWrite, `{"method":"account/rateLimits/updated","params":{"n":1}}`+"\n")
		}
		_, _ = io.WriteString(serverWrite, `{"id":1,"result":{"ok":true}}`+"\n")
	}()
	var out struct {
		OK bool `json:"ok"`
	}
	if err := client.Call(ctx, "test", nil, &out); err != nil {
		t.Fatalf("notification flood blocked response delivery: %v", err)
	}
	if !out.OK {
		t.Fatal("response after notification flood was not delivered")
	}
}

func TestRPCErrorStringDoesNotEchoRemoteMessageOrData(t *testing.T) {
	errText := (&RPCError{Code: 1234, Message: "Bearer super-secret-token", Data: json.RawMessage(`{"refresh_token":"secret"}`)}).Error()
	if strings.Contains(errText, "super-secret-token") || strings.Contains(errText, "refresh_token") || errText != "app-server RPC error 1234" {
		t.Fatalf("RPC error string exposed remote payload: %q", errText)
	}
}

func TestClientCloseFailsFutureCallsImmediately(t *testing.T) {
	clientRead, serverWrite := io.Pipe()
	serverRead, clientWrite := io.Pipe()
	defer serverWrite.Close()
	defer serverRead.Close()
	defer clientWrite.Close()

	client := NewClient(clientRead, clientWrite, 64*1024)
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	var out map[string]any
	if err := client.Call(ctx, "test", nil, &out); err == nil {
		t.Fatal("closed client must reject future calls")
	}
}

func TestClientDuplicateResponseFailsProtocolWithoutBlockingReadLoop(t *testing.T) {
	clientRead, serverWrite := io.Pipe()
	serverRead, clientWrite := io.Pipe()
	defer serverWrite.Close()
	defer serverRead.Close()
	defer clientWrite.Close()

	client := NewClient(clientRead, clientWrite, 64*1024)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	callDone := make(chan error, 1)
	go func() {
		var out map[string]any
		callDone <- client.Call(ctx, "test", nil, &out)
	}()

	scanner := bufio.NewScanner(serverRead)
	if !scanner.Scan() {
		t.Fatal("client did not send request")
	}
	frame, err := json.Marshal(map[string]any{"id": 1, "result": map[string]any{"ok": true}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := serverWrite.Write(append(frame, '\n')); err != nil {
		t.Fatal(err)
	}
	if _, err := serverWrite.Write(append(frame, '\n')); err != nil {
		t.Fatal(err)
	}

	select {
	case <-client.done:
	case <-ctx.Done():
		t.Fatal("duplicate response did not terminate the protocol client")
	}
	select {
	case <-callDone:
	case <-ctx.Done():
		t.Fatal("pending call did not resolve after duplicate response")
	}
}

func TestClientRejectsResponseWithBothResultAndError(t *testing.T) {
	clientRead, serverWrite := io.Pipe()
	serverRead, clientWrite := io.Pipe()
	defer serverWrite.Close()
	defer serverRead.Close()
	defer clientWrite.Close()

	client := NewClient(clientRead, clientWrite, 64*1024)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	callDone := make(chan error, 1)
	go func() {
		var out map[string]any
		callDone <- client.Call(ctx, "test", nil, &out)
	}()
	if !bufio.NewScanner(serverRead).Scan() {
		t.Fatal("client did not send request")
	}
	frame, err := json.Marshal(map[string]any{
		"id":     1,
		"result": map[string]any{},
		"error":  map[string]any{"code": -1, "message": "invalid"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := serverWrite.Write(append(frame, '\n')); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-callDone:
		if err == nil || !strings.Contains(err.Error(), "exactly one") {
			t.Fatalf("expected malformed response failure, got %v", err)
		}
	case <-ctx.Done():
		t.Fatal("malformed response left call blocked")
	}
}

func TestCallCancellationInterruptsClosableBlockedWriter(t *testing.T) {
	clientRead, serverWrite := io.Pipe()
	defer serverWrite.Close()
	writer := &blockingWriteCloser{started: make(chan struct{}), unblock: make(chan struct{})}
	client := NewClient(clientRead, writer, 64*1024)

	ctx, cancel := context.WithCancel(context.Background())
	callDone := make(chan error, 1)
	go func() {
		var out map[string]any
		callDone <- client.Call(ctx, "test", nil, &out)
	}()
	<-writer.started
	cancel()
	select {
	case err := <-callDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context cancellation, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("blocked writer was not interrupted by cancellation")
	}
}

type countingWriter struct {
	mu     sync.Mutex
	writes int
}

func (w *countingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	w.writes++
	w.mu.Unlock()
	return len(p), nil
}

func (w *countingWriter) Count() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.writes
}

type shortWriter struct{}

func (shortWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	return len(p) - 1, nil
}

type blockingWriteCloser struct {
	started   chan struct{}
	unblock   chan struct{}
	startOnce sync.Once
	closeOnce sync.Once
}

func (w *blockingWriteCloser) Write([]byte) (int, error) {
	w.startOnce.Do(func() { close(w.started) })
	<-w.unblock
	return 0, io.ErrClosedPipe
}

func (w *blockingWriteCloser) Close() error {
	w.closeOnce.Do(func() { close(w.unblock) })
	return nil
}
