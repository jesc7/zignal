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
	MT_NOANSWER      MessageType = iota - 3 //не отправлять ответ
	MT_PING                                 //ping
	MT_PONG                                 //pong
	MT_SENDOFFER                            //клиент1 отправил offer
	MT_SENDAUTH                             //клиент2 отправил auth
	MT_SENDANSWER                           //клиент2 отправил answer
	MT_RECEIVEANSWER                        //клиенту1 отправили answer клиента2
)

var (
	upgrader                  = websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	ErrClientNotFound         = errors.New("Клиент не найден")
	ErrClientAlreadyConnected = errors.New("Клиент уже установил соединение")
	ErrKeyNotFound            = errors.New("Ключ/пароль не найдены")
	ErrUnacceptableCommand    = errors.New("Недопустимая команда")
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
		t1 := time.NewTicker(1 * time.Minute)

		for {
			select {
			case <-quit:
				cancel()
				return

			case <-ctx.Done():
				return

			case <-t1.C:
				for _, с := range clients {
					func() {
						defer func() {
							if msg := recover(); msg != nil {
								delete(keys, с.key)
								delete(clients, с.conn)
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
	Error string      `json:"error,omitzero"`
	Key   string      `json:"key,omitzero"`
	Value string      `json:"val,omitzero"`
}

type Client struct {
	key       string
	pwd       string
	offer     string
	conn      *websocket.Conn
	pair      *websocket.Conn
	busy      bool
	heartbeat int64
}

func (c *Client) IsOfferer() bool { return c.offer != "" }

var (
	mu      sync.Mutex
	keys    = make(map[string]*websocket.Conn)
	clients = make(map[*websocket.Conn]*Client)
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
		return nil, ErrKeyNotFound
	}
	conn, ok := keys[sl[0]] //ищем в мапе ключей
	if !ok {
		return nil, ErrKeyNotFound
	}
	c, ok := clients[conn] //ищем в мапе клиентов
	if !ok || c.pwd != sl[1] {
		return nil, ErrKeyNotFound
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
	clients[conn] = me
	mu.Unlock()

	defer func() {
		delete(keys, key)
		delete(clients, conn)
	}()

	for {
		var msg Msg
		if e := conn.ReadJSON(&msg); e != nil {
			log.Printf("Read message error: %v", e)
			break
		}

		log.Printf("IN:  %#v", msg)

		if exit, _ := func() (exit bool, e error) {
			answer := Msg{Type: msg.Type}

			defer func() {
				if e != nil {
					log.Println(e)
					answer.Code = -1
					answer.Value = e.Error()
					time.Sleep(5 * time.Second)
				}
				if answer.Type >= 0 {
					log.Printf("OUT: %#v", answer)

					if e = conn.WriteJSON(answer); e != nil {
						log.Println(e)
					}
				}
			}()

			var client *Client
			switch msg.Type {
			case MT_PONG:
				me.heartbeat = time.Now().Unix()

			case MT_SENDOFFER: //клиент1 отправил offer, в ответ шлем key и password
				me.offer = msg.Value
				me.pair = nil
				me.busy = false
				answer.Key = me.key + "@" + me.pwd

			case MT_SENDAUTH: //клиент2 отправил auth (пару ключ@пароль)
				me.offer = "" //клиент перестает быть Offerer и становится Answerer
				me.pair = nil
				if client, e = getClient(msg.Key); e != nil || !client.IsOfferer() || client.busy {
					return false, ErrClientNotFound
				}
				me.pair = client.conn
				answer.Value = client.offer //авторизация пройдена, отдаем клиенту2 offer клиента1

			case MT_SENDANSWER: //клиент2 отправил answer для клиента1, пересылаем клиенту1
				if me.pair == nil {
					return false, ErrUnacceptableCommand
				}
				if e = me.pair.WriteJSON(Msg{ //шлем клиенту1 answer клиента2
					Type:  MT_RECEIVEANSWER,
					Value: msg.Value,
				}); e != nil {
					return
				}
				exit = true //клиенту2 сигнальный сервер больше не нужен, выходим

			case MT_RECEIVEANSWER: //клиент1 подтвердил получение answer
				exit = true //клиенту1 сигнальный сервер больше не нужен, выходим

			default:
				answer.Type = MT_NOANSWER
				log.Printf("Wrong type: %d", msg.Type)
			}
			return
		}(); exit {
			break
		}
	}
}
