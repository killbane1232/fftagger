package store

/*
#cgo darwin CFLAGS: -mmacosx-version-min=13.0
#cgo darwin LDFLAGS: -mmacosx-version-min=13.0
#cgo LDFLAGS: -lsqlite3
#include <sqlite3.h>
#include <stdlib.h>
static int bind_text(sqlite3_stmt *s, int i, const char *v, int n) { return sqlite3_bind_text(s,i,v,n,SQLITE_TRANSIENT); }
static int bind_blob(sqlite3_stmt *s, int i, const void *v, int n) { return sqlite3_bind_blob(s,i,v,n,SQLITE_TRANSIENT); }
*/
import "C"

import (
	"fmt"
	"unsafe"
)

type connection struct{ ptr *C.sqlite3 }
type row []any

func openSQLite(path string) (*connection, error) {
	p := C.CString(path)
	defer C.free(unsafe.Pointer(p))
	c := &connection{}
	if C.sqlite3_open_v2(p, &c.ptr, C.SQLITE_OPEN_READWRITE|C.SQLITE_OPEN_CREATE|C.SQLITE_OPEN_FULLMUTEX, nil) != C.SQLITE_OK {
		err := c.err()
		c.close()
		return nil, err
	}
	C.sqlite3_busy_timeout(c.ptr, 5000)
	return c, nil
}
func (c *connection) err() error {
	return fmt.Errorf("SQLite: %s", C.GoString(C.sqlite3_errmsg(c.ptr)))
}
func (c *connection) close() { C.sqlite3_close_v2(c.ptr) }
func (c *connection) query(sql string, args ...any) ([]row, error) {
	q := C.CString(sql)
	defer C.free(unsafe.Pointer(q))
	var s *C.sqlite3_stmt
	if C.sqlite3_prepare_v2(c.ptr, q, -1, &s, nil) != C.SQLITE_OK {
		return nil, c.err()
	}
	defer C.sqlite3_finalize(s)
	for i, a := range args {
		var rc C.int
		switch v := a.(type) {
		case nil:
			rc = C.sqlite3_bind_null(s, C.int(i+1))
		case string:
			p := C.CString(v)
			rc = C.bind_text(s, C.int(i+1), p, C.int(len(v)))
			C.free(unsafe.Pointer(p))
		case []byte:
			if len(v) == 0 {
				rc = C.sqlite3_bind_zeroblob(s, C.int(i+1), 0)
			} else {
				p := C.CBytes(v)
				rc = C.bind_blob(s, C.int(i+1), p, C.int(len(v)))
				C.free(p)
			}
		case int:
			rc = C.sqlite3_bind_int64(s, C.int(i+1), C.sqlite3_int64(v))
		case int64:
			rc = C.sqlite3_bind_int64(s, C.int(i+1), C.sqlite3_int64(v))
		case float64:
			rc = C.sqlite3_bind_double(s, C.int(i+1), C.double(v))
		default:
			return nil, fmt.Errorf("unsupported SQLite value %T", a)
		}
		if rc != C.SQLITE_OK {
			return nil, c.err()
		}
	}
	rows := []row{}
	for {
		rc := C.sqlite3_step(s)
		if rc == C.SQLITE_DONE {
			return rows, nil
		}
		if rc != C.SQLITE_ROW {
			return nil, c.err()
		}
		r := make(row, int(C.sqlite3_column_count(s)))
		for i := range r {
			switch C.sqlite3_column_type(s, C.int(i)) {
			case C.SQLITE_INTEGER:
				r[i] = int64(C.sqlite3_column_int64(s, C.int(i)))
			case C.SQLITE_FLOAT:
				r[i] = float64(C.sqlite3_column_double(s, C.int(i)))
			case C.SQLITE_TEXT:
				r[i] = C.GoStringN((*C.char)(unsafe.Pointer(C.sqlite3_column_text(s, C.int(i)))), C.sqlite3_column_bytes(s, C.int(i)))
			case C.SQLITE_BLOB:
				r[i] = C.GoBytes(C.sqlite3_column_blob(s, C.int(i)), C.sqlite3_column_bytes(s, C.int(i)))
			}
		}
		rows = append(rows, r)
	}
}
