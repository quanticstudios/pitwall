package decide

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/BurntSushi/toml"
)

// KeyEnv is the variable TypeSafe's SDKs read. Set, it wins over the
// credentials file.
const KeyEnv = "TYPESAFE_API_KEY"

// Key sources, for status lines.
const (
	FromEnv  = "environment"
	FromFile = "credentials file"
)

// CredentialsPath is the credentials file in the config directory dir. It
// sits next to config.toml but apart from it, as people share dotfiles.
func CredentialsPath(dir string) string { return filepath.Join(dir, "credentials") }

type credentials struct {
	TypeSafe string `toml:"typesafe_api_key"`
}

// LoadKey returns the TypeSafe key and where it came from: $TYPESAFE_API_KEY,
// else the file at path. No key is "", "" and no error. On Unix a file that
// other users can read is refused, as ssh refuses such a key.
func LoadKey(path string) (key, source string, err error) {
	if k := strings.TrimSpace(os.Getenv(KeyEnv)); k != "" {
		return k, FromEnv, nil
	}
	fi, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", "", nil
	}
	if err != nil {
		return "", "", err
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm()&0o077 != 0 {
		return "", "", fmt.Errorf("%s can be read by other users; run chmod 600 on it", path)
	}
	if di, err := os.Stat(filepath.Dir(path)); err == nil && runtime.GOOS != "windows" && di.Mode().Perm()&0o022 != 0 {
		return "", "", fmt.Errorf("%s can be changed by other users, who could swap the credentials file; run chmod 700 on it", filepath.Dir(path))
	}
	var c credentials
	if _, err := toml.DecodeFile(path, &c); err != nil {
		return "", "", fmt.Errorf("%s: not a valid credentials file", path)
	}
	if c.TypeSafe == "" {
		return "", "", nil
	}
	return c.TypeSafe, FromFile, nil
}

// CheckKey reports what is wrong with a key someone typed or pasted.
func CheckKey(key string) error {
	switch {
	case key == "":
		return errors.New("the key is empty")
	case len(key) < 8 || len(key) > 512:
		return errors.New("that does not look like an API key")
	case strings.ContainsFunc(key, func(r rune) bool { return r <= ' ' || r == 0x7f || r == '"' || r == '\\' }):
		return errors.New("the key has spaces or quotes in it")
	}
	return nil
}

// SaveKey writes key to the credentials file at path with mode 0600. On
// Unix the directory is made private (0700) when others can read or
// change it. On Windows pitwall sets no ACL: the file inherits the ACL of
// its folder under the user profile, which by default keeps other users
// out but is not checked.
func SaveKey(path, key string) error {
	if err := CheckKey(key); err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if runtime.GOOS != "windows" {
		di, err := os.Stat(dir)
		if err != nil {
			return err
		}
		if di.Mode().Perm()&0o077 != 0 {
			if err := os.Chmod(dir, di.Mode().Perm()&^0o077); err != nil {
				return fmt.Errorf("make %s private: %w", dir, err)
			}
		}
	}
	f, err := os.CreateTemp(dir, ".credentials-*") // created 0600
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err := f.Chmod(0o600); err != nil && runtime.GOOS != "windows" {
		return err
	}
	data := "# pitwall credentials. Keep this file private; never commit it.\ntypesafe_api_key = \"" + key + "\"\n"
	if _, err := f.WriteString(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

// DeleteKey removes the credentials file. A missing one is no error.
func DeleteKey(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
