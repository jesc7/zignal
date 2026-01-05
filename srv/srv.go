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

const (
	MT_SENDOFFER     = iota //клиент1 отправил offer
	MT_SENDAUTH             //клиент2 отправил auth
	MT_SENDANSWER           //клиент2 отправил answer
	MT_RECEIVEANSWER        //клиенту1 отправили answer клиента2
)

var (
	upgrader = websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
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

		select {
		case <-quit:
			cancel()
		case <-ctx.Done():
		}
	}()

	wg.Wait()
	return nil
}

type Msg struct {
	Type  int    `json:"type"`
	Code  int    `json:"code"`
	Error string `json:"error,omitzero"`
	Key   string `json:"key,omitzero"`
	Value string `json:"val,omitzero"`
}

type Client struct {
	key          string
	pwd          string
	isOfferer    bool
	payload      string
	answererConn *websocket.Conn
}

var (
	mut     sync.Mutex
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

func handleWS(w http.ResponseWriter, r *http.Request) {
	conn, e := upgrader.Upgrade(w, r, nil)
	if e != nil {
		log.Printf("error: %v", e)
		w.WriteHeader(http.StatusUpgradeRequired)
		return
	}

	mut.Lock()
	key, e := generateKey(6) //генерим ключ
	if e != nil {
		mut.Unlock()
		log.Printf("Generate key error: %v", e)
		time.Sleep(3 * time.Second)
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(e.Error()))
		return
	}

	keys[key] = conn //добавляем клиента в коллекцию
	clients[conn] = &Client{
		key:       key,
		pwd:       util.RandomString(4, ""),
		isOfferer: true,
	}
	mut.Unlock()

	defer func() {
		delete(keys, clients[conn].key)
		delete(clients, conn)
	}()

	for {
		var msg Msg
		if e := conn.ReadJSON(&msg); e != nil {
			log.Printf("Read message error: %v", e)
			break
		}

		log.Printf("IN:  %#v", msg)

		func() (e error) {
			receiver, msgAnswer, needAnswer := &websocket.Conn{}, Msg{Type: msg.Type}, true
			defer func() {
				if e != nil {
					msgAnswer.Code = -1
					msgAnswer.Value = e.Error()
					time.Sleep(3 * time.Second)
				}
				if needAnswer {
					log.Printf("OUT: %#v", msgAnswer)

					if e = receiver.WriteJSON(msgAnswer); e != nil {
						log.Printf("error: %v", e)
					}
				}
			}()

			_auth := func(key string) (c *Client, e error) {
				e = errors.New("Ключ/пароль не найдены")
				sl := strings.Split(key, "@")
				if len(sl) < 2 {
					return
				}
				conn, ok := keys[sl[0]] //ищем в мапе ключей
				if !ok {
					return
				}
				if c, ok = clients[conn]; !ok || c.pwd != sl[1] { //ищем в мапе клиентов
					return
				}
				return c, nil
			}

			switch msg.Type {
			case MT_SENDOFFER: //клиент отправил offer, в ответ шлем key и password
				receiver = conn
				client := clients[conn]
				client.isOfferer = true
				client.payload = msg.Value
				msgAnswer.Key = client.key + "@" + client.pwd

			case MT_SENDAUTH: //клиент отправил auth
				receiver = conn
				sl := strings.Split(msg.Key, "@")
				if len(sl) < 2 {
					log.Printf("Wrong key: %s", msg.Key)
					return errors.New("Ключ/пароль не найдены")
				}

				key, pwd, ok := sl[0], sl[1], false
				receiver, ok = keys[key] //ищем в мапе ключей
				if !ok {
					log.Printf("Key not found: %s", msg.Key)
					return errors.New("Ключ/пароль не найдены")
				}

				client, ok := clients[receiver] //ищем в мапе клиентов
				if !ok || client.pwd != pwd {
					log.Printf("Client not found, key@pwd: %s", msg.Key)
					return errors.New("Ключ/пароль не найдены")
				}

				msgAnswer.Value = client.payload //авторизация пройдена, отдаем offer

			case MT_SENDANSWER: //клиент отправил answer
				msgAnswer.Type = MT_RECEIVEANSWER
				msgAnswer.Value = msg.Value

			default:
				needAnswer = false
				log.Printf("Wrong type: %d", msg.Type)
			}
			return nil
		}()
	}
}
