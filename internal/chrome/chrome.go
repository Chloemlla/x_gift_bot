package chrome

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha1"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	_ "github.com/mattn/go-sqlite3"
	"golang.org/x/crypto/pbkdf2"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"time"
)

// Extract only the two X authentication cookies. No other site's credentials are read.
func Extract(profile string) ([]byte, error) {
	if profile != "Default" && !regexp.MustCompile(`^Profile [0-9]+$`).MatchString(profile) {
		return nil, errors.New("invalid Chrome profile directory")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	path := filepath.Join(home, "Library/Application Support/Google/Chrome", profile, "Cookies")
	if _, err = os.Stat(path); err != nil {
		path = filepath.Join(filepath.Dir(path), "Network", "Cookies")
	}
	u := url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro"}
	db, err := sql.Open("sqlite3", u.String())
	if err != nil {
		return nil, err
	}
	defer db.Close()
	var versionText string
	if err = db.QueryRow("SELECT value FROM meta WHERE key='version'").Scan(&versionText); err != nil {
		return nil, err
	}
	version, err := strconv.Atoi(versionText)
	if err != nil {
		return nil, err
	}
	secret, err := exec.Command("security", "find-generic-password", "-w", "-s", "Chrome Safe Storage").Output()
	if err != nil {
		return nil, errors.New("Chrome Safe Storage unavailable")
	}
	defer clear(secret)
	key := pbkdf2.Key(bytes.TrimSuffix(secret, []byte("\n")), []byte("saltysalt"), 1003, 16, sha1.New)
	defer clear(key)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	rows, err := db.Query("SELECT host_key,name,value,encrypted_value,path,is_secure,is_httponly,expires_utc,samesite FROM cookies WHERE host_key IN ('.x.com','x.com') AND name IN ('auth_token','ct0')")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cookies := []map[string]any{}
	found := map[string]bool{}
	for rows.Next() {
		var host, name, value, path string
		var encrypted []byte
		var secure, httpOnly, sameSite int
		var expires int64
		if err = rows.Scan(&host, &name, &value, &encrypted, &path, &secure, &httpOnly, &expires, &sameSite); err != nil {
			return nil, err
		}
		if len(encrypted) > 0 {
			if !bytes.HasPrefix(encrypted, []byte("v10")) || (len(encrypted)-3)%aes.BlockSize != 0 || len(encrypted) < 19 {
				return nil, errors.New("unsupported cookie encryption")
			}
			raw := make([]byte, len(encrypted)-3)
			cipher.NewCBCDecrypter(block, bytes.Repeat([]byte(" "), 16)).CryptBlocks(raw, encrypted[3:])
			pad := int(raw[len(raw)-1])
			if pad < 1 || pad > 16 || !bytes.Equal(raw[len(raw)-pad:], bytes.Repeat([]byte{byte(pad)}, pad)) {
				return nil, errors.New("cookie decryption failed")
			}
			raw = raw[:len(raw)-pad]
			if version >= 24 {
				digest := sha256.Sum256([]byte(host))
				if len(raw) < 32 || !bytes.Equal(raw[:32], digest[:]) {
					return nil, errors.New("cookie integrity check failed")
				}
				raw = raw[32:]
			}
			value = string(raw)
			clear(raw)
		}
		expiry := float64(-1)
		if expires > 0 {
			expiry = float64(expires)/1e6 - 11644473600
			if expiry < float64(time.Now().Unix()) {
				return nil, fmt.Errorf("X cookie %s expired", name)
			}
		}
		same := "Lax"
		if sameSite == 0 {
			same = "None"
		}
		if sameSite == 2 {
			same = "Strict"
		}
		cookies = append(cookies, map[string]any{"name": name, "value": value, "domain": host, "path": path, "secure": secure != 0, "httpOnly": httpOnly != 0, "expires": expiry, "sameSite": same})
		found[name] = true
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if !found["auth_token"] || !found["ct0"] {
		return nil, errors.New("required X cookies not found")
	}
	return json.Marshal(map[string]any{"cookies": cookies, "origins": []any{}})
}
