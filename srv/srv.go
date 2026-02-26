package srv

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/mux"
	"github.com/gorilla/websocket"
	"github.com/jesc7/zignal/util"
)

type MessageType int

const (
	MT_NOANSWER      MessageType = iota - 1 //не отправлять ответ
	MT_SENDOFFER                            //клиент1 отправил offer
	MT_SENDANSWER                           //клиент2 отправил answer
	MT_RECEIVEANSWER                        //клиенту1 отправили answer клиента2
	MT_CONNECT                              //клиент1 уведомляет об установлении соединения
	MT_DISCONNECT                           //клиент1 уведомляет о разрыве соединения
	MT_PING                                 //ping
	MT_PONG                                 //pong
)

var (
	upgrader               = websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	ErrClientNotFound      = errors.New("Клиент не найден")
	ErrClientBusy          = errors.New("Клиент занят")
	ErrKeyNotFound         = errors.New("Ключ/пароль не найдены")
	ErrUnacceptableCommand = errors.New("Недопустимая команда")
)

func Start(ctx context.Context, service bool) error {
	bin, e := runPath(service)
	if e != nil {
		return e
	}

	type Config struct {
		Port int
	}
	cfg := Config{
		Port: 1212,
	}

	if util.IsFileExists(filepath.Join(filepath.Dir(bin), "cfg.json")) {
		f, e := os.ReadFile(filepath.Join(filepath.Dir(bin), "cfg.json"))
		if e != nil {
			return e
		}
		if e = json.Unmarshal(f, &cfg); e != nil {
			return e
		}
	}

	server := &http.Server{Addr: fmt.Sprintf(":%d", cfg.Port)}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	wg := &sync.WaitGroup{}
	wg.Add(2)

	go func() {
		defer func() {
			cancel()
			wg.Done()
		}()

		r := mux.NewRouter()
		r.HandleFunc("/ws", handleWS)
		server.Handler = r
		if e = server.ListenAndServe(); e != nil {
			log.Printf("error: %v", e)
		}
	}()

	go func() {
		defer func() {
			server.Shutdown(ctx)
			wg.Done()
		}()

		quit := make(chan os.Signal, 2)
		defer close(quit)
		signal.Notify(quit, os.Interrupt, syscall.SIGTERM)
		t1m := time.NewTicker(1 * time.Minute)

		for {
			select {
			case <-quit:
				cancel()
				return

			case <-ctx.Done():
				return

			case <-t1m.C:
				for _, с := range conns {
					func() {
						defer func() {
							if msg := recover(); msg != nil {
								delete(keys, с.key)
								delete(conns, с.conn)
							}
						}()
						с.conn.WriteJSON(Msg{Type: MT_PING})
					}()
				}
			}
		}
	}()

	wg.Wait()
	return nil
}

type Msg struct {
	Type  MessageType `json:"type"`
	Code  int         `json:"code"`
	Key   string      `json:"key,omitzero"`
	Value string      `json:"val,omitzero"`
}

type Client struct {
	key       string
	pwd       string
	offer     string
	conn      *websocket.Conn
	busy      bool
	heartbeat int64
}

func (c *Client) IsOfferer() bool { return c.offer != "" }

var (
	mu    sync.Mutex
	keys  = make(map[string]*websocket.Conn)
	conns = make(map[*websocket.Conn]*Client)
)

func generateKey(length int) (string, error) {
	for range 1000 {
		key := util.RandomString(length, "0123456789")
		if _, ok := keys[key]; !ok {
			return key, nil
		}
	}
	return "", errors.New("error key generate")
}

func getClient(key string) (*Client, error) {
	sl := strings.Split(key, "@")
	if len(sl) < 2 {
		return nil, ErrClientNotFound
	}
	conn, ok := keys[sl[0]] //ищем в мапе ключей
	if !ok {
		return nil, ErrClientNotFound
	}
	c, ok := conns[conn] //ищем в мапе клиентов
	if !ok || c.pwd != sl[1] {
		return nil, ErrClientNotFound
	}
	return c, nil
}

func handleWS(w http.ResponseWriter, r *http.Request) {
	conn, e := upgrader.Upgrade(w, r, nil)
	if e != nil {
		log.Printf("error: %v", e)
		w.WriteHeader(http.StatusUpgradeRequired)
		return
	}

	mu.Lock()
	key, e := generateKey(8) //генерим ключ
	if e != nil {
		mu.Unlock()
		log.Printf("Generate key error: %v", e)
		time.Sleep(5 * time.Second)
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(e.Error()))
		return
	}

	keys[key] = conn //добавляем клиента в коллекцию
	me := &Client{
		key:  key,
		pwd:  util.RandomString(4, "0123456789"),
		conn: conn,
	}
	conns[conn] = me
	mu.Unlock()

	defer func() {
		delete(keys, key)
		delete(conns, conn)
	}()

	for {
		var in Msg
		if e := conn.ReadJSON(&in); e != nil {
			log.Printf("Read message error: %v", e)
			break
		}

		log.Printf("IN:  %#v", in)

		if exit, _ := func() (exit bool, e error) {
			out := Msg{Type: in.Type}

			defer func() {
				if e != nil {
					log.Println(e)
					out.Code = -1
					out.Value = e.Error()
					time.Sleep(5 * time.Second)
				}
				if out.Type > MT_NOANSWER {
					log.Printf("OUT: %#v", out)
					if e = conn.WriteJSON(out); e != nil {
						log.Println(e)
					}
				}
			}()

			var client *Client
			switch in.Type {
			case MT_PONG:
				me.heartbeat = time.Now().Unix()
				out.Type = MT_NOANSWER

			case MT_SENDOFFER: //клиент1 отправил offer, в ответ шлем key и password
				me.busy = false
				me.offer = in.Value
				out.Key = me.key + "@" + me.pwd

			case MT_SENDANSWER: //клиент2 отправил answer (key=ключ@пароль, value=answer)
				me.offer = "" //клиент2 перестает быть Offerer и становится Answerer
				if client, e = getClient(in.Key); e != nil {
					return
				}
				if !client.IsOfferer() || client.busy {
					return false, ErrClientBusy
				}
				if e = client.conn.WriteJSON(Msg{ //шлем клиенту1 answer клиента2
					Type:  MT_RECEIVEANSWER,
					Value: in.Value,
				}); e != nil {
					return
				}
				out.Value = client.offer //авторизация пройдена, отдаем клиенту2 offer клиента1
				exit = true              //клиенту2 сигнальный сервер больше не нужен, выходим

			case MT_RECEIVEANSWER: //клиент1 подтвердил получение answer
				//тут нет логики, возможно позже удалю
				out.Type = MT_NOANSWER

			case MT_CONNECT, MT_DISCONNECT: //клиент1 установил/разорвал соединение
				me.busy = in.Type == MT_CONNECT
				out.Type = MT_NOANSWER

			default:
				log.Printf("Wrong type: %d", in.Type)
				out.Type = MT_NOANSWER
			}
			return
		}(); exit {
			break
		}
	}
}
