package srv

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"

	"github.com/gorilla/mux"
	"github.com/gorilla/websocket"

	"github.com/jesc7/zsdp/store"
	_ "github.com/jesc7/zsdp/store/sqlite"
	"github.com/jesc7/zsdp/util"
)

const (
	MT_SENDOFFER     = iota //клиент1 отправил offer
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

	if store.Store == nil {
		return errors.New("Store wasnt initialized")
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
	Value string `json:"val,omitzero"`
}

type Client struct {
	key          string
	pwd          string
	isOfferer    bool
	sdp          string
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

	key, e := generateKey(6) //генерим ключ
	if e != nil {
		log.Printf("error: %v", e)
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(e.Error()))
		return
	}

	mut.Lock() //добавляем клиента в коллекцию
	keys[key] = conn
	clients[conn] = &Client{
		key:       key,
		pwd:       util.RandomString(4, ""),
		isOfferer: true,
	}
	client := clients[conn]
	mut.Unlock()

	for {
		var msg Msg
		if e := conn.ReadJSON(&msg); e != nil {
			log.Printf("error: %v", e)
			break
		}

		log.Printf("IN:  %#v", msg)

		func() (e error) {
			answer, needAnswer := Msg{Type: msg.Type}, true
			defer func() {
				if e != nil {
					answer.Code = -1
					answer.Value = e.Error()
				}
				if needAnswer {
					log.Printf("OUT: %#v", answer)

					if e = conn.WriteJSON(answer); e != nil {
						log.Printf("error: %v", e)
					}
				}
			}()

			switch msg.Type {
			case MT_SENDOFFER: //клиент отправил offer, в ответ шлем key и password
				answer.Key, answer.Pwd, e = store.Store.SendOffer(msg.Value, conn)

			case MT_SENDANSWER: //клиент отправил answer
				var obj any
				obj, e = store.Store.SendAnswer(msg.Key, msg.Pwd, msg.Value)
				if e != nil {
					return
				}
				c, ok := obj.(*websocket.Conn)
				if !ok {
					e = errors.New("wrong obj type")
					return
				}
				c.WriteJSON(Msg{
					Type:  MT_RECEIVEANSWER,
					Value: msg.Value,
				})

			default:
				needAnswer = false
				log.Printf("Wrong type: %d", msg.Type)
			}
			return nil
		}()
	}
}

func Encode(obj any) (string, error) {
	b, e := json.Marshal(obj)
	if e != nil {
		return "", e
	}
	if b, e = util.Zip(b); e != nil {
		return "", e
	}
	return base64.StdEncoding.EncodeToString(b), nil
}

func Decode(in string, obj any) error {
	b, e := base64.StdEncoding.DecodeString(in)
	if e != nil {
		return e
	}
	if b, e = util.Unzip(b); e != nil {
		return e
	}
	return json.Unmarshal(b, obj)
}
