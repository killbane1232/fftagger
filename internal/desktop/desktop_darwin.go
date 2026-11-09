//go:build darwin

package desktop

/*
#cgo CFLAGS: -mmacosx-version-min=13.0
#cgo LDFLAGS: -mmacosx-version-min=13.0
#cgo LDFLAGS: -framework Cocoa -framework WebKit
#include <stdlib.h>
void fftagger_run(const char *url);
char *fftagger_choose_folder(void);
void fftagger_set_theme(int mode);
*/
import "C"
import "unsafe"

func Run(url string) { p := C.CString(url); defer C.free(unsafe.Pointer(p)); C.fftagger_run(p) }
func SetTheme(theme string) {
	mode := 0
	if theme == "dark" {
		mode = 2
	} else if theme == "light" {
		mode = 1
	}
	C.fftagger_set_theme(C.int(mode))
}
func ChooseFolder() (string, error) {
	p := C.fftagger_choose_folder()
	if p == nil {
		return "", nil
	}
	defer C.free(unsafe.Pointer(p))
	return C.GoString(p), nil
}
