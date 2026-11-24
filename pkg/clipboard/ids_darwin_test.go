package clipboard

import (
	"unsafe"

	"github.com/ebitengine/purego/objc"
)

var keep [][]objc.ID

func unsafeIDs(ids ...objc.ID) unsafe.Pointer {
	keep = append(keep, ids)
	return unsafe.Pointer(&ids[0])
}
