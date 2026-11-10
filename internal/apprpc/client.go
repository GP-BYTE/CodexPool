package apprpc

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"sync"
	"time"
)

const DefaultMaxFrameBytes = 2 << 20

type RPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *RPCError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("app-server RPC error %d", e.Code)
}

type Notification struct {
	Method string
	Params json.RawMessage
}

type responseEnvelope struct {
	Result   json.RawMessage `json:"result,omitempty"`
	Error    *RPCError       `json:"error,omitempty"`
	LocalErr error           `json:"-"`
}

type wireEnvelope struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *RPCError       `json:"error,omitempty"`
}

type Client struct {
	reader       *bufio.Reader
	readerCloser io.Closer
	writer       io.Writer
	writerCloser io.Closer
	maxFrame     int

	writeMu   sync.Mutex
	mu        sync.Mutex
	nextID    uint64
	pending   map[uint64]chan responseEnvelope
	responded map[uint64]bool

	notifications chan Notification
	done          chan struct{}
	closeOnce     sync.Once
	ioCloseOnce   sync.Once
	closeErr      error
}

func NewClient(reader io.Reader, writer io.Writer, maxFrame int) *Client {
	if maxFrame <= 0 {
		maxFrame = DefaultMaxFrameBytes
	}
	c := &Client{
		reader:        bufio.NewReaderSize(reader, 64*1024),
		writer:        writer,
		maxFrame:      maxFrame,
		pending:       make(map[uint64]chan responseEnvelope),
		responded:     make(map[uint64]bool),
		notifications: make(chan Notification, 32),
		done:          make(chan struct{}),
	}
	if closer, ok := reader.(io.Closer); ok {
		c.readerCloser = closer
	}
	if closer, ok := writer.(io.Closer); ok {
		c.writerCloser = closer
	}
	go c.readLoop()
	return c
}

func (c *Client) Notifications() <-chan Notification { return c.notifications }

func (c *Client) Initialize(ctx context.Context, clientName, title, version string) error {
	params := map[string]any{
		"clientInfo": map[string]string{
			"name":    clientName,
			"title":   title,
			"version": version,
		},
	}
	var ignored json.RawMessage
	if err := c.Call(ctx, "initialize", params, &ignored); err != nil {
		return fmt.Errorf("initialize app server: %w", err)
	}
	if err := c.notifyContext(ctx, "initialized", map[string]any{}); err != nil {
		return fmt.Errorf("send initialized notification: %w", err)
	}
	return nil
}

func (c *Client) Call(ctx context.Context, method string, params any, out any) error {
	if method == "" {
		return errors.New("RPC method is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	id, ch, err := c.registerPending()
	if err != nil {
		return err
	}
	defer c.removePending(id)

	request := map[string]any{"id": id, "method": method}
	if params != nil {
		request["params"] = params
	}
	if err := c.writeJSONContext(ctx, request); err != nil {
		return err
	}

	return c.waitCallResponse(ctx, ch, method, out)
}

func (c *Client) waitCallResponse(ctx context.Context, ch <-chan responseEnvelope, method string, out any) error {
	select {
	case response := <-ch:
		return finishCallResponse(method, response, out)
	case <-ctx.Done():
		// If the response was already delivered when the deadline/cancellation
		// became observable, prefer the complete response over a synthetic timeout.
		select {
		case response := <-ch:
			return finishCallResponse(method, response, out)
		default:
			return ctx.Err()
		}
	case <-c.done:
		// readLoop may deliver the final complete response and then immediately
		// observe EOF. Never let the close signal win over an already-buffered
		// response for this call.
		select {
		case response := <-ch:
			return finishCallResponse(method, response, out)
		default:
			return c.closedError()
		}
	}
}

func finishCallResponse(method string, response responseEnvelope, out any) error {
	if response.LocalErr != nil {
		return response.LocalErr
	}
	if response.Error != nil {
		return response.Error
	}
	if out == nil || len(response.Result) == 0 || bytes.Equal(response.Result, []byte("null")) {
		return nil
	}
	if raw, ok := out.(*json.RawMessage); ok {
		*raw = append((*raw)[:0], response.Result...)
		return nil
	}
	if err := json.Unmarshal(response.Result, out); err != nil {
		return fmt.Errorf("decode %s response: %w", method, err)
	}
	return nil
}

func (c *Client) Notify(method string, params any) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return c.notifyContext(ctx, method, params)
}

func (c *Client) notifyContext(ctx context.Context, method string, params any) error {
	if method == "" {
		return errors.New("RPC notification method is required")
	}
	request := map[string]any{"method": method}
	if params != nil {
		request["params"] = params
	}
	return c.writeJSONContext(ctx, request)
}

func (c *Client) Close() error {
	c.failAll(io.EOF)
	return c.closeIO()
}

func (c *Client) registerPending() (uint64, chan responseEnvelope, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	select {
	case <-c.done:
		return 0, nil, c.closedErrorLocked()
	default:
	}
	c.nextID++
	id := c.nextID
	ch := make(chan responseEnvelope, 1)
	c.pending[id] = ch
	return id, ch, nil
}

func (c *Client) removePending(id uint64) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

func (c *Client) writeJSONContext(ctx context.Context, value any) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	payload, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode RPC frame: %w", err)
	}
	if len(payload) > c.maxFrame {
		return fmt.Errorf("outbound RPC frame exceeds %d bytes", c.maxFrame)
	}
	payload = append(payload, '\n')

	result := make(chan error, 1)
	go func() {
		c.writeMu.Lock()
		defer c.writeMu.Unlock()
		select {
		case <-c.done:
			result <- c.closedError()
			return
		default:
		}
		n, writeErr := c.writer.Write(payload)
		if writeErr != nil {
			result <- fmt.Errorf("write RPC frame: %w", writeErr)
			return
		}
		if n != len(payload) {
			result <- fmt.Errorf("write RPC frame: %w (%d of %d bytes)", io.ErrShortWrite, n, len(payload))
			return
		}
		result <- nil
	}()

	select {
	case err := <-result:
		return err
	case <-ctx.Done():
		c.failAll(ctx.Err())
		_ = c.closeIO()
		return ctx.Err()
	case <-c.done:
		_ = c.closeIO()
		return c.closedError()
	}
}

func (c *Client) closeIO() error {
	var closeErr error
	c.ioCloseOnce.Do(func() {
		if c.writerCloser != nil {
			if err := c.writerCloser.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
				closeErr = err
			}
		}
		if c.readerCloser != nil {
			if err := c.readerCloser.Close(); err != nil && !errors.Is(err, os.ErrClosed) && closeErr == nil {
				closeErr = err
			}
		}
	})
	return closeErr
}

func (c *Client) readLoop() {
	for {
		line, err := c.readLineLimited()
		if err != nil {
			c.failAll(err)
			return
		}
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		if err := c.handleLine(line); err != nil {
			c.failAll(err)
			return
		}
	}
}

func (c *Client) readLineLimited() ([]byte, error) {
	var out []byte
	for {
		fragment, err := c.reader.ReadSlice('\n')
		if len(out)+len(fragment) > c.maxFrame+1 {
			return nil, fmt.Errorf("inbound RPC frame exceeds %d bytes", c.maxFrame)
		}
		out = append(out, fragment...)
		switch {
		case err == nil:
			return bytes.TrimSuffix(out, []byte{'\n'}), nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF) && len(out) > 0:
			return nil, errors.New("truncated RPC frame before newline")
		default:
			return nil, err
		}
	}
}

func (c *Client) handleLine(line []byte) error {
	var envelope wireEnvelope
	if err := json.Unmarshal(line, &envelope); err != nil {
		return fmt.Errorf("decode RPC frame: %w", err)
	}

	if envelope.Method != "" {
		if len(envelope.ID) != 0 && string(envelope.ID) != "null" {
			return c.rejectServerRequest(envelope.ID)
		}
		n := Notification{Method: envelope.Method, Params: append(json.RawMessage(nil), envelope.Params...)}
		select {
		case c.notifications <- n:
		default:
			// Notifications are advisory. Dropping a burst is safer than blocking
			// responses; callers retain periodic full reads as the source of truth.
		}
		return nil
	}

	if len(envelope.ID) == 0 {
		return errors.New("RPC response missing id")
	}
	hasResult := len(envelope.Result) != 0
	hasError := envelope.Error != nil
	if hasResult == hasError {
		return errors.New("RPC response must contain exactly one of result or error")
	}
	id, err := parseNumericID(envelope.ID)
	if err != nil {
		return err
	}
	c.mu.Lock()
	ch := c.pending[id]
	if c.responded[id] {
		c.mu.Unlock()
		return fmt.Errorf("duplicate RPC response id %d", id)
	}
	if ch == nil {
		c.mu.Unlock()
		return nil
	}
	c.responded[id] = true
	c.mu.Unlock()
	select {
	case ch <- responseEnvelope{Result: envelope.Result, Error: envelope.Error}:
		return nil
	default:
		return fmt.Errorf("RPC response channel unexpectedly full for id %d", id)
	}
}

func (c *Client) rejectServerRequest(id json.RawMessage) error {
	response := map[string]any{
		"id": json.RawMessage(id),
		"error": map[string]any{
			"code":    -32601,
			"message": "CodexPool does not support server-initiated requests",
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	return c.writeJSONContext(ctx, response)
}

func parseNumericID(raw json.RawMessage) (uint64, error) {
	text := string(bytes.TrimSpace(raw))
	if text == "" {
		return 0, errors.New("empty RPC id")
	}
	id, err := strconv.ParseUint(text, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("unsupported non-numeric RPC id %q", text)
	}
	return id, nil
}

func (c *Client) failAll(err error) {
	if err == nil {
		err = io.EOF
	}
	c.closeOnce.Do(func() {
		c.mu.Lock()
		c.closeErr = err
		for _, ch := range c.pending {
			select {
			case ch <- responseEnvelope{LocalErr: err}:
			default:
			}
		}
		close(c.done)
		c.mu.Unlock()
	})
}

func (c *Client) closedError() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closedErrorLocked()
}

func (c *Client) closedErrorLocked() error {
	if c.closeErr == nil {
		return errors.New("app-server client closed")
	}
	return c.closeErr
}
