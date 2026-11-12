package nex

import (
	"fmt"
	"runtime/debug"
)

// * 届いたデータの読み取りで想定外の panic が起きても、サーバー全体を落とさない。
// * そのパケットだけを捨てて、記録に残す。ok が false なら、途中で止まった
func safely(what string, f func()) (ok bool) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Printf("[YW2-SAFE] %s: recovered from panic: %v\n%s", what, r, debug.Stack())
			ok = false
		}
	}()
	f()
	return true
}
