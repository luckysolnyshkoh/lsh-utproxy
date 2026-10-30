package util

import "sync"

var BufPool = sync.Pool{
	New: func() any {
		b := make([]byte, 64*1024)
		return &b
	},
}

func GetBuf() *[]byte {
	return BufPool.Get().(*[]byte)
}

func PutBuf(p *[]byte) {
	if p != nil {
		BufPool.Put(p)
	}
}
