package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// StdioTransport 通过子进程 stdin/stdout 进行 JSON-RPC 通信。
//
// MCP stdio 传输使用 newline-delimited JSON 格式：
// 每条消息是一行完整的 JSON，以 '\n' 结尾。
// 消息中不能包含嵌入式换行符。
type StdioTransport struct {
	name string

	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
	reader *bufio.Reader

	mu      sync.Mutex
	reqID   atomic.Int64
	pending map[int64]chan *jsonrpcResponse

	closed  atomic.Bool
	closeCh chan struct{}
}

// newStdioTransport 创建传输层实例（不启动进程）
func newStdioTransport(name string) *StdioTransport {
	return &StdioTransport{
		name:    name,
		pending: make(map[int64]chan *jsonrpcResponse),
		closeCh: make(chan struct{}),
	}
}

// Start 启动子进程并建立管道
func (t *StdioTransport) Start(command string, args []string, env map[string]string, workDir string) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.cmd != nil {
		return fmt.Errorf("MCP server %s already started", t.name)
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
	t.stdin, err = cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("create stdin pipe for %s: %w", t.name, err)
	}

	t.stdout, err = cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("create stdout pipe for %s: %w", t.name, err)
	}

	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("create stderr pipe for %s: %w", t.name, err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start MCP server %s: %w", t.name, err)
	}

	go func() {
		scanner := bufio.NewScanner(stderrPipe)
		for scanner.Scan() {
			log.Printf("[MCP:%s] %s", t.name, scanner.Text())
		}
	}()

	t.cmd = cmd
	t.reader = bufio.NewReaderSize(t.stdout, 1<<20) // 1MB buffer

	// 后台读取服务器响应
	go t.readLoop()

	return nil
}

// Stop 终止子进程
func (t *StdioTransport) Stop() error {
	if t.closed.Swap(true) {
		return nil
	}
	close(t.closeCh)

	// 先通知所有 pending 请求（不持锁，避免死锁）
	t.failAllPending(fmt.Errorf("MCP server %s stopped", t.name))

	t.mu.Lock()
	defer t.mu.Unlock()

	if t.stdin != nil {
		t.stdin.Close()
	}
	if t.stdout != nil {
		t.stdout.Close()
	}
	if t.cmd != nil && t.cmd.Process != nil {
		_ = t.cmd.Process.Kill()
		// 等待进程退出（清理僵尸进程）
		_ = t.cmd.Wait()
	}

	t.cmd = nil
	return nil
}

// SendRequest 发送 JSON-RPC 请求并等待响应。
// ctx 用于取消请求；closeCh 用于检测服务器关闭；120s 超时作为兜底。
func (t *StdioTransport) SendRequest(ctx context.Context, method string, params interface{}) (json.RawMessage, error) {
	paramsJSON, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("marshal params for %s: %w", method, err)
	}

	id := t.reqID.Add(1)
	req := jsonrpcRequest{
		JSONRPC: "2.0",
		ID:      id,
		Method:  method,
		Params:  paramsJSON,
	}

	respCh := make(chan *jsonrpcResponse, 1)

	t.mu.Lock()
	t.pending[id] = respCh
	t.mu.Unlock()

	defer func() {
		t.mu.Lock()
		delete(t.pending, id)
		t.mu.Unlock()
	}()

	if err := t.writeMessage(req); err != nil {
		return nil, fmt.Errorf("write request '%s': %w", method, err)
	}

	// 等待响应（ctx 取消 > 服务器关闭 > 120s 超时）
	select {
	case resp := <-respCh:
		if resp.Error != nil {
			return nil, resp.Error
		}
		return resp.Result, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("MCP request '%s' cancelled: %w", method, ctx.Err())
	case <-t.closeCh:
		return nil, fmt.Errorf("MCP server %s closed", t.name)
	case <-time.After(120 * time.Second):
		return nil, fmt.Errorf("MCP request '%s' timed out after 120s (no context deadline set)", method)
	}
}

// SendNotification 发送 JSON-RPC 通知（不等待响应）
func (t *StdioTransport) SendNotification(method string, params interface{}) error {
	paramsJSON, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("marshal params for %s: %w", method, err)
	}

	notif := jsonrpcNotification{
		JSONRPC: "2.0",
		Method:  method,
		Params:  paramsJSON,
	}
	return t.writeMessage(notif)
}

// IsRunning 检查子进程是否仍在运行
func (t *StdioTransport) IsRunning() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.cmd == nil || t.cmd.Process == nil {
		return false
	}
	return true
}

// ---------------------------------------------------------------------------
// 内部方法
// ---------------------------------------------------------------------------

// writeMessage 写入 MCP 协议格式的消息
func (t *StdioTransport) writeMessage(msg interface{}) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.stdin == nil || t.closed.Load() {
		return fmt.Errorf("transport %s is closed", t.name)
	}

	body, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal message: %w", err)
	}

	// MCP stdio: JSON + newline
	body = append(body, '\n')
	if _, err := t.stdin.Write(body); err != nil {
		return fmt.Errorf("write message: %w", err)
	}
	return nil
}

// readLoop 持续从服务器 stdout 读取响应
func (t *StdioTransport) readLoop() {
	for {
		select {
		case <-t.closeCh:
			return
		default:
		}

		msg, err := t.readMessage()
		if err != nil {
			if t.closed.Load() {
				return
			}
			// 任何读取错误都表示连接已破坏
			t.failAllPending(fmt.Errorf("MCP server %s process terminated: %w", t.name, err))
			return
		}

		if msg == nil {
			continue
		}

		t.dispatchMessage(msg)
	}
}

// readMessage 读取一个 newline-delimited JSON 消息
func (t *StdioTransport) readMessage() (json.RawMessage, error) {
	if t.reader == nil {
		return nil, fmt.Errorf("reader is nil")
	}

	const maxSkipLines = 10000
	lineCount := 0

	for {
		lineCount++
		if lineCount > maxSkipLines {
			return nil, fmt.Errorf("exceeded %d lines without finding valid JSON-RPC message", maxSkipLines)
		}

		line, err := t.reader.ReadString('\n')
		if err != nil {
			if err == io.EOF && t.closed.Load() {
				return nil, nil
			}
			return nil, err
		}

		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			continue // 跳过空行
		}

		// 检查是否为有效的 JSON（以 '{' 开头）
		if len(line) > 0 && line[0] == '{' {
			return json.RawMessage(line), nil
		}

		// 非 JSON 行（如服务器启动日志），跳过
	}
}

// dispatchMessage 分发 JSON-RPC 消息到对应的 pending channel
func (t *StdioTransport) dispatchMessage(msg json.RawMessage) {
	// 解析为响应（包含 result 或 error）
	var resp jsonrpcResponse
	if err := json.Unmarshal(msg, &resp); err == nil && resp.ID != 0 {
		t.mu.Lock()
		ch, ok := t.pending[resp.ID]
		t.mu.Unlock()
		if ok {
			ch <- &resp
			return
		}
	}

	// MCP 当前不处理来自服务器的请求/通知，静默忽略
}

// failAllPending 通知所有等待中的请求失败（由 readLoop 或 Stop 调用）
func (t *StdioTransport) failAllPending(reason error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for id, ch := range t.pending {
		select {
		case ch <- &jsonrpcResponse{Error: &jsonrpcError{Code: -1, Message: reason.Error()}}:
		default:
		}
		delete(t.pending, id)
	}
}
