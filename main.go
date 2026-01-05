package main

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jesc7/zsdp/srv"
	"github.com/kardianos/service"
)

var logger service.Logger

type program struct {
	service service.Service
	cancel  context.CancelFunc
}

func (p *program) Start(s service.Service) error {
	var ctx context.Context
	ctx, p.cancel = context.WithCancel(context.Background())
	go p.run(ctx)
	return nil
}

func (p *program) run(ctx context.Context) {
	defer func() {
		if service.Interactive() {
			p.Stop(p.service)
		} else {
			p.service.Stop()
		}
	}()

	if e := srv.Start(ctx, !service.Interactive()); e != nil {
		logger.Error(e)
	}
}

func (p *program) Stop(s service.Service) error {
	p.cancel()
	time.Sleep(500 * time.Millisecond)
	if service.Interactive() {
		os.Exit(0)
	}
	return nil
}

func main() { //11111111111111111
	//настроим логирование
	flog, e := os.OpenFile(strings.TrimSuffix(os.Args[0], filepath.Ext(os.Args[0]))+".log", os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0666)
	if e != nil {
		log.Fatalln(e)
	}
	defer flog.Close()
	log.SetOutput(flog)

	//конфиг сервиса
	p := &program{}
	s, e := service.New(p, &service.Config{
		Name:        "zignal",
		DisplayName: "Simple signaling server",
		Description: "Simple signaling server",
	})
	if e != nil {
		log.Fatal(e)
	}
	p.service = s

	if len(os.Args) > 1 {
		if e := service.Control(s, os.Args[1]); e != nil {
			log.Fatal(e)
		}
		return
	}

	if logger, e = s.Logger(nil); e != nil {
		log.Fatal(e)
	}
	if e = s.Run(); e != nil {
		logger.Error(e)
	}
}
