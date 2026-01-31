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
	MT_SENDOFFER     MessageType = iota //клиент1 отправил offer
	MT_SENDAUTH                         //клиент2 отправил auth
	MT_SENDANSWER                       //клиент2 отправил answer
	MT_RECEIVEANSWER                    //клиенту1 отправили answer клиента2
)

var (
	upgrader       = websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	ErrKeyNotFound = errors.New("Ключ/пароль не найдены")
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
	Type  MessageType `json:"type"`
	Code  int         `json:"code"`
	Error string      `json:"error,omitzero"`
	Key   string      `json:"key,omitzero"`
	Value string      `json:"val,omitzero"`
}

type Client struct {
	key          string
	pwd          string
	isOfferer    bool
	payload      string
	answererConn *websocket.Conn
}

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

func getOfferer(key string) (*Client, error) {
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

func checkType(c *Client, mt MessageType) error {
	switch c.isOfferer {
	case true:
		switch mt {
		case MT_SENDOFFER:
		case MT_RECEIVEANSWER:
		default:
			return errors.New("Несогласованная команда")
		}

	default:
		switch mt {
		case MT_SENDOFFER:
		case MT_SENDAUTH:
		case MT_SENDANSWER:
		default:
			return errors.New("Несогласованная команда")
		}
	}
	return nil
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
	clients[conn] = &Client{
		key:       key,
		pwd:       util.RandomString(4, "0123456789"),
		isOfferer: true, //изначально все клиенты - оффереры
	}
	mu.Unlock()

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

		if exit, _ := func() (exit bool, e error) {
			receiver, answer, needAnswer := &websocket.Conn{}, Msg{Type: msg.Type}, true
			defer func() {
				if e != nil {
					log.Println(e)
					answer.Code = -1
					answer.Value = e.Error()
					time.Sleep(5 * time.Second)
				}
				if needAnswer {
					log.Printf("OUT: %#v", answer)

					if e = receiver.WriteJSON(answer); e != nil {
						log.Println(e)
					}
				}
			}()

			var client *Client
			switch msg.Type {
			case MT_SENDOFFER: //клиент1 отправил offer, в ответ шлем key и password
				receiver = conn
				client = clients[conn]
				client.isOfferer = true
				client.payload = msg.Value
				answer.Key = client.key + "@" + client.pwd

			case MT_SENDAUTH: //клиент2 отправил auth
				receiver = conn
				clients[conn].isOfferer = false //клиент перестает быть офферером
				if client, e = getOfferer(msg.Key); e != nil {
					return
				}
				answer.Value = client.payload //авторизация пройдена, отдаем offer клиента1

			case MT_SENDANSWER: //клиент2 отправил answer, пересылаем клиенту1
				client, e = getOfferer(msg.Key)
				if e != nil {
					return
				}
				receiver = keys[client.key]
				answer.Type = MT_RECEIVEANSWER
				answer.Value = msg.Value

				go func() {
					defer func() { recover() }()
					time.Sleep(10 * time.Second)
					receiver.Close() //принудительно закрываем соединение клиента1 через 10сек, т.к. сведение пиров завершено
				}()

				if e = conn.WriteJSON(Msg{ //шлем ответ клиенту2, что все ок
					Type: msg.Type,
					Key:  msg.Key,
				}); e != nil {
					log.Printf("Error: %v", e)
				}
				exit = true //клиенту2 сигнальный сервер больше не нужен, выходим

			case MT_RECEIVEANSWER: //клиент1 подтвердил получение answer
				exit = true //клиенту1 сигнальный сервер больше не нужен, выходим

			default:
				needAnswer = false
				log.Printf("Wrong type: %d", msg.Type)
			}
			return
		}(); exit {
			break
		}
	}
}
