package main

import (
	"context"
	"log"
	"time"

	"lite-agent/bot"
	"lite-agent/server"
)

// serverProgram 将 *server.Server 适配为 service.Program（启动非阻塞）。
type serverProgram struct {
	srv *server.Server
	// autoTelegram 为 true 时在服务就绪后自动拉起 Telegram Bot（both 模式）
	autoTelegram bool
}

func (p *serverProgram) Start() error {
	go func() {
		if err := p.srv.Start(); err != nil {
			log.Printf("WebSocket 服务退出: %v", err)
		}
	}()

	if p.autoTelegram {
		go func() {
			// 稍作等待，确保 HTTP 服务已开始监听
			time.Sleep(500 * time.Millisecond)
			if err := p.srv.StartTelegramBot(); err != nil {
				log.Printf("⚠️  自动启动 Telegram Bot 失败: %v", err)
			}
		}()
	}
	return nil
}

func (p *serverProgram) Stop() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return p.srv.Shutdown(ctx)
}

// botProgram 将 *bot.Bot 适配为 service.Program（仅 Telegram 模式）。
type botProgram struct {
	bot *bot.Bot
}

func (p *botProgram) Start() error {
	go func() {
		if err := p.bot.StartWithoutSignal(); err != nil {
			log.Printf("Telegram Bot 退出: %v", err)
		}
	}()
	return nil
}

func (p *botProgram) Stop() error {
	// 保存会话；Bot 的长轮询循环随进程退出而结束
	p.bot.Shutdown()
	return nil
}
