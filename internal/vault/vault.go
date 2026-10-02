package vault

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "github.com/mattn/go-sqlite3"
	"golang.org/x/crypto/scrypt"
)

type Vault struct {
	db   *sql.DB
	aead cipher.AEAD
}

func NewPassword() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	f, err := os.CreateTemp("/tmp", "xgift-password-*")
	if err != nil {
		return "", err
	}
	path := f.Name()
	if _, err = f.WriteString(base64.RawURLEncoding.EncodeToString(b) + "\n"); err != nil {
		f.Close()
		os.Remove(path)
		return "", err
	}
	if err = f.Close(); err != nil {
		return "", err
	}
	return path, nil
}

func Open(path, passwordFile string, create bool) (*Vault, error) {
	info, err := os.Lstat(passwordFile)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("password file must be a regular owner-only file (0600)")
	}
	password, err := os.ReadFile(passwordFile)
	if err != nil {
		return nil, err
	}
	password = []byte(strings.TrimSpace(string(password)))
	defer clear(password)
	if len(password) < 32 {
		return nil, errors.New("password is too short")
	}
	if create {
		if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return nil, err
		}
		if err = os.Chmod(filepath.Dir(path), 0700); err != nil {
			return nil, err
		}
		f, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			return nil, e
		}
		f.Close()
	}
	info, err = os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("database must be a regular owner-only file")
	}
	db, err := sql.Open("sqlite3", path+"?_busy_timeout=5000&_journal_mode=DELETE&_secure_delete=on&_synchronous=FULL")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	ok := false
	defer func() {
		if !ok {
			db.Close()
		}
	}()
	var salt []byte
	if create {
		_, err = db.Exec(`CREATE TABLE metadata (key TEXT PRIMARY KEY, value BLOB NOT NULL); CREATE TABLE secrets (name TEXT PRIMARY KEY, payload BLOB NOT NULL);`)
		if err != nil {
			return nil, err
		}
		salt = make([]byte, 32)
		if _, err = rand.Read(salt); err != nil {
			return nil, err
		}
		_, err = db.Exec("INSERT INTO metadata VALUES ('salt', ?)", salt)
		if err != nil {
			return nil, err
		}
	} else {
		if err = db.QueryRow("SELECT value FROM metadata WHERE key='salt'").Scan(&salt); err != nil {
			return nil, err
		}
	}
	if len(salt) != 32 {
		return nil, errors.New("invalid vault salt")
	}
	key, err := scrypt.Key(password, salt, 32768, 8, 1, 32)
	if err != nil {
		return nil, err
	}
	defer clear(key)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	v := &Vault{db, aead}
	if create {
		err = v.Put("vault-check", []byte("xgift-v1"))
	} else {
		var b []byte
		b, err = v.Get("vault-check")
		if err == nil && string(b) != "xgift-v1" {
			err = errors.New("invalid vault version")
		}
	}
	if err != nil {
		return nil, errors.New("vault authentication failed (wrong password or corrupted database)")
	}
	ok = true
	return v, nil
}

func (v *Vault) Put(name string, plain []byte) error {
	nonce := make([]byte, v.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	encrypted := v.aead.Seal(nonce, nonce, plain, []byte("xgift-v1:"+name))
	_, err := v.db.Exec("INSERT INTO secrets(name,payload) VALUES (?,?) ON CONFLICT(name) DO UPDATE SET payload=excluded.payload", name, encrypted)
	return err
}
func (v *Vault) Get(name string) ([]byte, error) {
	var b []byte
	if err := v.db.QueryRow("SELECT payload FROM secrets WHERE name=?", name).Scan(&b); err != nil {
		return nil, fmt.Errorf("secret %s unavailable: %w", name, err)
	}
	n := v.aead.NonceSize()
	if len(b) < n {
		return nil, errors.New("invalid encrypted record")
	}
	return v.aead.Open(nil, b[:n], b[n:], []byte("xgift-v1:"+name))
}
func (v *Vault) Close() error { return v.db.Close() }

// Archive atomically preserves an authenticated record under a new name and removes
// its active key, only if its contents still match the verified snapshot.
func (v *Vault) Archive(name, archive string, expected, archived []byte) error {
	tx, err := v.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var payload []byte
	if err = tx.QueryRow("SELECT payload FROM secrets WHERE name=?", name).Scan(&payload); err != nil {
		return err
	}
	n := v.aead.NonceSize()
	if len(payload) < n {
		return errors.New("invalid encrypted record")
	}
	plain, err := v.aead.Open(nil, payload[:n], payload[n:], []byte("xgift-v1:"+name))
	if err != nil {
		return err
	}
	defer clear(plain)
	if !bytes.Equal(plain, expected) {
		return errors.New("order changed since verification")
	}
	nonce := make([]byte, n)
	if _, err = rand.Read(nonce); err != nil {
		return err
	}
	encrypted := v.aead.Seal(nonce, nonce, archived, []byte("xgift-v1:"+archive))
	if _, err = tx.Exec("INSERT INTO secrets(name,payload) VALUES (?,?)", archive, encrypted); err != nil {
		return err
	}
	if _, err = tx.Exec("DELETE FROM secrets WHERE name=?", name); err != nil {
		return err
	}
	return tx.Commit()
}
