//go:build !darwin

package desktop

import "errors"

func Run(url string)        {}
func SetTheme(theme string) {}
func ChooseFolder() (string, error) {
	return "", errors.New("нативный выбор папки доступен только в macOS")
}
