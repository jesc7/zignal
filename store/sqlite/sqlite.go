package sqlite

import (
	"database/sql"
	"errors"
	"math/rand"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/jesc7/zsdp/store"
	"github.com/jesc7/zsdp/util"
	_ "github.com/mattn/go-sqlite3"
)

type SQLiteStore struct {
	db *sql.DB
}

var (
	keys = make(map[int]any)
	rnd  = rand.New(rand.NewSource(time.Now().UnixNano()))
	mut  sync.Mutex
)

func init() {
	st, _ := newSQLiteStore("./store.db")
	st.db.Exec(`delete from sdps where persist = 0`)
	store.Store = st
}

func newSQLiteStore(dbName string) (*SQLiteStore, error) {
	store := &SQLiteStore{}
	var e error
	if store.db, e = sql.Open("sqlite3", dbName); e != nil {
		return nil, e
	}
	_, e = store.db.Exec(`
		create table if not exists sdps (
			inserted integer default (unixepoch()),
			key integer primary key,
			pwd text not null,
			token text,
			caption text,
			offer text
		);`)
	return store, e
}

func GenerateKey() (int, error) {
	const (
		KEYVALUE_MIN = 100_000
		KEYVALUE_MAX = 999_999
	)
	var key int //генерируем ключ
	for i, found := 0, true; i <= 1000 && found; _, found = keys[key] {
		key, i = rnd.Intn(KEYVALUE_MAX-KEYVALUE_MIN+1)+KEYVALUE_MIN, i+1
	}
	if key == 0 {
		return 0, "", errors.New("key doesnt generated")
	}
}

func (store *SQLiteStore) SendOffer(value string, args ...any) (int, string, error) {
	const (
		KEYVALUE_MIN = 100_000
		KEYVALUE_MAX = 999_999
	)

	mut.Lock()
	defer mut.Unlock()

	if len(args) < 1 {
		return 0, "", errors.New("arg #1 needed")
	}
	obj, ok := args[0].(*websocket.Conn)
	if !ok {
		return 0, "", errors.New("arg #1 has wrong type")
	}

	var key int //генерируем ключ
	for i, found := 0, true; i <= 1000 && found; _, found = keys[key] {
		key, i = rnd.Intn(KEYVALUE_MAX-KEYVALUE_MIN+1)+KEYVALUE_MIN, i+1
	}
	if key == 0 {
		return 0, "", errors.New("key doesnt generated")
	}
	keys[key] = obj
	pwd := util.RandomString(4) //генерируем пароль

	_, e := store.db.Exec("insert into sdps (key, pwd, offer) values (?, ?, ?)", key, pwd, value)
	return key, pwd, e
}

func (store *SQLiteStore) SendAnswer(key int, pwd, value string, args ...any) (any, error) {
	obj, ok := keys[key]
	if !ok {
		return nil, errors.New("offer not found")
	}

	var cnt int
	e := store.db.QueryRow(`select 1 from sdps where key = ? and pwd = ?`, key, pwd).Scan(&cnt)
	if e != nil || cnt == 0 {
		return nil, errors.New("offer not found")
	}
	return obj, nil
}
