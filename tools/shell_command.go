package tools

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"sync"
	"time"
)

// ShellStatus 命令执行状态
type ShellStatus int

const (
	ShellStatusRunning     ShellStatus = iota
	ShellStatusCompleted               // 正常退出
	ShellStatusKilled                  // 被 kill
	ShellStatusBackgrounded            // 超时后台化，进程仍在运行
)

// ShellResult 命令执行结果
type ShellResult struct {
	Stdout      string
	Stderr      string
	ExitCode    int
	Interrupted bool   // 被 kill 终止
	Backgrounded bool  // 已后台化，输出为部分内容
	TimedOut    bool   // 超时触发
}

// OnTimeoutFn 超时回调，返回 true 表示后台化，false 表示 kill
type OnTimeoutFn func(backgroundFn func()) bool

// ShellCommand 包装一个正在执行的 shell 进程
type ShellCommand struct {
	mu     sync.Mutex
	status ShellStatus

	cmd    *exec.Cmd
	outBuf *lockedBuffer // stdout+stderr 合并输出，线程安全

	result chan ShellResult // 执行完成后写入一次

	// 超时自动后台化支持
	onTimeout      OnTimeoutFn
	timeoutTimer   *time.Timer
	backgroundCh   chan struct{} // 被后台化时关闭
	cancelWatchdog context.CancelFunc
}

// lockedBuffer 线程安全的 bytes.Buffer
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (n int, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// spawnShellCommand 启动 shell 命令，返回 ShellCommand 句柄
// timeoutMs: 超时毫秒数，<=0 表示不超时
// onTimeout: 超时回调，返回 true 后台化，false 直接 kill；nil 则直接 kill
func spawnShellCommand(ctx context.Context, command string, timeoutMs int, onTimeout OnTimeoutFn) (*ShellCommand, error) {
	sc := &ShellCommand{
		status:     ShellStatusRunning,
		outBuf:     &lockedBuffer{},
		result:     make(chan ShellResult, 1),
		onTimeout:  onTimeout,
		backgroundCh: make(chan struct{}),
	}

	// 构造 cmd
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-Command", command)
	} else {
		cmd = exec.Command("sh", "-c", command)
	}

	// 关闭 stdin，防止交互式命令永久等待
	cmd.Stdin = nil

	// 输出写入线程安全 buffer
	cmd.Stdout = sc.outBuf
	cmd.Stderr = sc.outBuf

	// 设置进程组，便于后续 tree-kill（平台特定实现）
	setSysProcAttr(cmd)

	sc.cmd = cmd

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("启动命令失败: %w", err)
	}

	// 设置超时
	if timeoutMs > 0 {
		sc.timeoutTimer = time.AfterFunc(time.Duration(timeoutMs)*time.Millisecond, func() {
			sc.handleTimeout()
		})
	}

	// 后台 goroutine 等待进程结束
	go sc.waitProcess(ctx)

	return sc, nil
}

// handleTimeout 超时触发
func (sc *ShellCommand) handleTimeout() {
	sc.mu.Lock()
	if sc.status != ShellStatusRunning {
		sc.mu.Unlock()
		return
	}
	sc.mu.Unlock()

	// 调用超时回调，决定后台化还是 kill
	if sc.onTimeout != nil {
		backgrounded := sc.onTimeout(func() {
			sc.doBackground()
		})
		if backgrounded {
			return
		}
	}
	// 默认：先 SIGTERM，稍后 SIGKILL
	sc.doKill(false)
}

// doBackground 将进程转为后台，关闭 backgroundCh 通知 waitProcess
func (sc *ShellCommand) doBackground() {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	if sc.status != ShellStatusRunning {
		return
	}
	sc.status = ShellStatusBackgrounded
	close(sc.backgroundCh)
}

// doKill 终止进程（整个进程组）
func (sc *ShellCommand) doKill(gentle bool) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	if sc.status != ShellStatusRunning && sc.status != ShellStatusBackgrounded {
		return
	}
	sc.status = ShellStatusKilled

	if sc.cmd.Process == nil {
		return
	}

	killProcessGroup(sc.cmd.Process, gentle)
}

// Kill 外部主动终止命令
func (sc *ShellCommand) Kill() {
	if sc.timeoutTimer != nil {
		sc.timeoutTimer.Stop()
	}
	sc.doKill(false)
}

// waitProcess 等待进程退出，写入 result
func (sc *ShellCommand) waitProcess(ctx context.Context) {
	// 用独立 channel 等待 cmd.Wait()
	doneCh := make(chan error, 1)
	go func() {
		doneCh <- sc.cmd.Wait()
	}()

	var waitErr error
	select {
	case waitErr = <-doneCh:
		// 进程正常/异常退出
	case <-sc.backgroundCh:
		// 已后台化：不等进程，返回当前已有输出
		// 继续让进程在后台运行，由 GC 或 agent 退出时清理
		sc.emitResult(0, true, false)
		// 仍需消费 doneCh 防止 goroutine 泄漏
		go func() { <-doneCh }()
		return
	case <-ctx.Done():
		// context 取消（agent 退出）：kill 进程
		sc.doKill(false)
		waitErr = <-doneCh
	}

	// 停止超时定时器
	if sc.timeoutTimer != nil {
		sc.timeoutTimer.Stop()
	}

	sc.mu.Lock()
	timedOut := sc.status == ShellStatusKilled
	sc.mu.Unlock()

	// 确保状态更新
	sc.mu.Lock()
	if sc.status == ShellStatusRunning {
		sc.status = ShellStatusCompleted
	}
	sc.mu.Unlock()

	exitCode := 0
	if waitErr != nil {
		if exitErr, ok := waitErr.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = 1
		}
	}
	interrupted := exitCode == -1 || (sc.status == ShellStatusKilled)
	sc.emitResult(exitCode, false, timedOut || interrupted)
}

// emitResult 写入结果（只写一次）
func (sc *ShellCommand) emitResult(exitCode int, backgrounded bool, interrupted bool) {
	output := sc.outBuf.String()

	// 限制输出长度
	const maxOutputBytes = 10000
	if len(output) > maxOutputBytes {
		output = output[:maxOutputBytes] + "\n... (输出被截断)"
	}

	sc.result <- ShellResult{
		Stdout:       output,
		ExitCode:     exitCode,
		Interrupted:  interrupted,
		Backgrounded: backgrounded,
		TimedOut:     interrupted && !backgrounded,
	}
}

// Result 等待命令完成，返回结果（带二次超时保护）
func (sc *ShellCommand) Result(waitTimeout time.Duration) ShellResult {
	select {
	case r := <-sc.result:
		return r
	case <-time.After(waitTimeout):
		// 二次保护：等待结果本身超时，强制 kill 并返回当前输出
		sc.doKill(false)
		select {
		case r := <-sc.result:
			return r
		case <-time.After(5 * time.Second):
			// 最坏情况：进程处于 D state，直接返回已有输出
			return ShellResult{
				Stdout:      sc.outBuf.String(),
				ExitCode:    -1,
				Interrupted: true,
				TimedOut:    true,
			}
		}
	}
}
