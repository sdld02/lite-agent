package lsp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// LSPClient 封装与 LSP 服务器的 JSON-RPC 通信。
//
// 实现 LSP 的 Content-Length header 协议：
//
//	Content-Length: <length>\r\n
//	\r\n
//	<json>
type LSPClient struct {
	name       string
	cmd        *exec.Cmd
	stdin      io.WriteCloser
	stdout     io.ReadCloser
	reader     *bufio.Reader

	mu         sync.Mutex
	reqID      atomic.Int64
	pending    map[int64]chan *jsonrpcResponse
	handlers   map[string]func(json.RawMessage) (interface{}, error)
	notifyHandlers map[string]func(json.RawMessage)

	isInitialized atomic.Bool
	closed        atomic.Bool
	closeCh       chan struct{}
}

// jsonrpcRequest JSON-RPC 2.0 请求
type jsonrpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// jsonrpcNotification JSON-RPC 2.0 通知（无 id）
type jsonrpcNotification struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// jsonrpcResponse JSON-RPC 2.0 响应
type jsonrpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *jsonrpcError   `json:"error,omitempty"`
}

// jsonrpcError JSON-RPC 错误
type jsonrpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// LSPContentModified 是 rust-analyzer 等服务器索引期间的瞬时错误码
const LSPContentModified = -32801

func (e *jsonrpcError) isContentModified() bool {
	return e.Code == LSPContentModified
}

// NewLSPClient 创建 LSP 客户端（不启动进程）
func NewLSPClient(name string) *LSPClient {
	return &LSPClient{
		name:           name,
		pending:        make(map[int64]chan *jsonrpcResponse),
		handlers:       make(map[string]func(json.RawMessage) (interface{}, error)),
		notifyHandlers: make(map[string]func(json.RawMessage)),
		closeCh:        make(chan struct{}),
	}
}

// Start 启动 LSP 服务器子进程
func (c *LSPClient) Start(command string, args []string, env map[string]string, workDir string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.cmd != nil {
		return fmt.Errorf("LSP server %s already started", c.name)
	}

	cmd := exec.Command(command, args...)
	if workDir != "" {
		cmd.Dir = workDir
	}

	// 设置环境变量
	if len(env) > 0 {
		cmd.Env = cmd.Environ()
		for k, v := range env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}

	var err error
	c.stdin, err = cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("failed to create stdin pipe for %s: %w", c.name, err)
	}

	c.stdout, err = cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("failed to create stdout pipe for %s: %w", c.name, err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start LSP server %s: %w", c.name, err)
	}

	c.cmd = cmd
	c.reader = bufio.NewReader(c.stdout)

	// 后台读取服务器响应
	go c.readLoop()

	return nil
}

// Stop 关闭 LSP 服务器子进程
func (c *LSPClient) Stop() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed.Swap(true) {
		return nil // 已关闭
	}
	close(c.closeCh)

	if c.stdin != nil {
		c.stdin.Close()
	}
	if c.stdout != nil {
		c.stdout.Close()
	}
	if c.cmd != nil && c.cmd.Process != nil {
		// 先优雅关闭
		_ = c.cmd.Process.Kill()
	}

	c.cmd = nil
	return nil
}

// Initialize 发送 LSP initialize 请求
func (c *LSPClient) Initialize(params interface{}) (*jsonrpcResponse, error) {
	paramsJSON, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("marshal initialize params: %w", err)
	}

	resp, err := c.sendRequest("initialize", paramsJSON)
	if err != nil {
		return nil, err
	}

	// 发送 initialized 通知
	if err := c.sendNotification("initialized", json.RawMessage("{}")); err != nil {
		return nil, fmt.Errorf("send initialized notification: %w", err)
	}

	c.isInitialized.Store(true)
	return resp, nil
}

// SendRequest 发送 LSP 请求并等待响应
func (c *LSPClient) SendRequest(method string, params interface{}) (json.RawMessage, error) {
	paramsJSON, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("marshal params for %s: %w", method, err)
	}

	resp, err := c.sendRequest(method, paramsJSON)
	if err != nil {
		return nil, err
	}

	if resp.Error != nil {
		return nil, resp.Error
	}
	return resp.Result, nil
}

// SendNotification 发送 LSP 通知（不等待响应）
func (c *LSPClient) SendNotification(method string, params interface{}) error {
	paramsJSON, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("marshal params for %s: %w", method, err)
	}
	return c.sendNotification(method, paramsJSON)
}

// OnRequest 注册处理来自服务器的请求
func (c *LSPClient) OnRequest(method string, handler func(json.RawMessage) (interface{}, error)) {
	c.handlers[method] = handler
}

// OnNotification 注册处理来自服务器的通知
func (c *LSPClient) OnNotification(method string, handler func(json.RawMessage)) {
	c.notifyHandlers[method] = handler
}

// IsInitialized 检查服务器是否已完成初始化
func (c *LSPClient) IsInitialized() bool {
	return c.isInitialized.Load()
}

// IsRunning 检查进程是否仍在运行
func (c *LSPClient) IsRunning() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cmd == nil || c.cmd.Process == nil {
		return false
	}
	return true
}

// ---------------------------------------------------------------------------
// 内部方法
// ---------------------------------------------------------------------------

// generateID 生成递增的请求 ID
func (c *LSPClient) generateID() int64 {
	return c.reqID.Add(1)
}

// sendRequest 发送 JSON-RPC 请求
func (c *LSPClient) sendRequest(method string, params json.RawMessage) (*jsonrpcResponse, error) {
	id := c.generateID()
	req := jsonrpcRequest{
		JSONRPC: "2.0",
		ID:      id,
		Method:  method,
		Params:  params,
	}

	respCh := make(chan *jsonrpcResponse, 1)

	c.mu.Lock()
	c.pending[id] = respCh
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()

	if err := c.writeMessage(req); err != nil {
		return nil, fmt.Errorf("write request '%s': %w", method, err)
	}

	// 等待响应（带超时）
	select {
	case resp := <-respCh:
		return resp, nil
	case <-c.closeCh:
		return nil, fmt.Errorf("LSP server %s closed", c.name)
	case <-time.After(30 * time.Second):
		return nil, fmt.Errorf("LSP request '%s' timed out after 30s", method)
	}
}

// sendNotification 发送 JSON-RPC 通知
func (c *LSPClient) sendNotification(method string, params json.RawMessage) error {
	notif := jsonrpcNotification{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
	}
	return c.writeMessage(notif)
}

// writeMessage 写入 LSP 协议格式的消息
func (c *LSPClient) writeMessage(msg interface{}) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.stdin == nil || c.closed.Load() {
		return fmt.Errorf("client %s is closed", c.name)
	}

	body, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal message: %w", err)
	}

	// LSP Header: Content-Length: <len>\r\n\r\n
	header := fmt.Sprintf("Content-Length: %d\r\n\r\n", len(body))
	if _, err := io.WriteString(c.stdin, header); err != nil {
		return fmt.Errorf("write header: %w", err)
	}
	if _, err := c.stdin.Write(body); err != nil {
		return fmt.Errorf("write body: %w", err)
	}
	return nil
}

// readLoop 持续从服务器 stdout 读取响应
func (c *LSPClient) readLoop() {
	for {
		select {
		case <-c.closeCh:
			return
		default:
		}

		msg, err := c.readMessage()
		if err != nil {
			if c.closed.Load() {
				return
			}
			continue
		}
		if msg == nil {
			continue
		}

		c.dispatchMessage(msg)
	}
}

// readMessage 读取一个 LSP 协议消息
func (c *LSPClient) readMessage() (json.RawMessage, error) {
	c.mu.Lock()
	reader := c.reader
	if c.stdout != nil {
		reader = bufio.NewReaderSize(c.stdout, 1<<20) // 1MB buffer
	}
	c.mu.Unlock()

	if reader == nil {
		return nil, nil
	}

	// 读取 Content-Length header
	var contentLength int
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF && c.closed.Load() {
				return nil, nil
			}
			return nil, err
		}

		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}

		if strings.HasPrefix(line, "Content-Length:") {
			val := strings.TrimSpace(strings.TrimPrefix(line, "Content-Length:"))
			contentLength, err = strconv.Atoi(val)
			if err != nil {
				return nil, fmt.Errorf("parse Content-Length '%s': %w", val, err)
			}
		}
	}

	if contentLength <= 0 {
		return nil, fmt.Errorf("no valid Content-Length header")
	}

	// 读取消息体
	body := make([]byte, contentLength)
	if _, err := io.ReadFull(reader, body); err != nil {
		return nil, fmt.Errorf("read message body: %w", err)
	}

	return body, nil
}

// dispatchMessage 分发 JSON-RPC 消息
func (c *LSPClient) dispatchMessage(msg json.RawMessage) {
	// 先尝试解析为响应
	var resp jsonrpcResponse
	if err := json.Unmarshal(msg, &resp); err == nil && resp.ID != 0 {
		c.mu.Lock()
		ch, ok := c.pending[resp.ID]
		c.mu.Unlock()
		if ok {
			ch <- &resp
			return
		}
	}

	// 尝试解析为请求（来自服务器）
	var req jsonrpcRequest
	if err := json.Unmarshal(msg, &req); err == nil && req.ID != 0 && req.Method != "" {
		c.mu.Lock()
		handler, ok := c.handlers[req.Method]
		c.mu.Unlock()
		if ok {
			result, err := handler(req.Params)
			if err != nil {
				return
			}
			// 发送响应
			respMsg := jsonrpcResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
			}
			respMsg.Result, _ = json.Marshal(result)
			if err := c.writeMessage(respMsg); err != nil {
				return
			}
		}
		return
	}

	// 尝试解析为通知
	var notif jsonrpcNotification
	if err := json.Unmarshal(msg, &notif); err == nil && notif.Method != "" {
		c.mu.Lock()
		handler, ok := c.notifyHandlers[notif.Method]
		c.mu.Unlock()
		if ok {
			handler(notif.Params)
		}
	}
}

// Error 实现 error 接口，使 jsonrpcError 可直接作为 error 返回
func (e *jsonrpcError) Error() string {
	return fmt.Sprintf("LSP error [%d]: %s", e.Code, e.Message)
}
